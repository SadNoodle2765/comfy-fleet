package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"log"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

var myEnv map[string]string

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
}

const offlineTime = 30 * time.Second

type Worker struct {
	WorkerID         string    `json:"worker_id"`
	URL              string    `json:"url"`
	GPU              *string   `json:"gpu"`
	VRAMGiB          *float64  `json:"vram_gib"`
	VRAMFreeGiB      *float64  `json:"vram_free_gib"`
	ComfyUIAvailable bool      `json:"comfyui_available"`
	LastSeen         time.Time `json:"last_seen"`
}

type WorkerStatus struct {
	Worker Worker `json:"worker"`
	Online bool   `json:"online"`
}

type SafeWorkerMap struct {
	mu        sync.RWMutex
	workerMap map[string]Worker
}

type Job struct {
	ID string `json:"job_id"`
}

type ListWorkersResponse struct {
	Workers []WorkerStatus `json:"workers"`
}

type ScheduleRequest struct {
	RequiredVRAMGiB float64 `json:"required_vram_gib"`
}

type ScheduleResponse struct {
	JobID  string `json:"job_id"`
	Worker Worker `json:"worker"`
}

type SendJobResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

func (w Worker) IsOnline() bool {
	return time.Since(w.LastSeen) < offlineTime
}

func (wMap *SafeWorkerMap) Get(workerID string) (Worker, bool) {
	wMap.mu.RLock()
	defer wMap.mu.RUnlock()

	worker, exists := wMap.workerMap[workerID]
	if !exists {
		return Worker{}, false
	}
	return worker, true
}

func (wMap *SafeWorkerMap) Put(worker Worker) (workerExists bool) {
	wMap.mu.Lock()
	defer wMap.mu.Unlock()

	_, workerExists = wMap.workerMap[worker.WorkerID]
	wMap.workerMap[worker.WorkerID] = worker

	return workerExists
}

func (wMap *SafeWorkerMap) GetWorker(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	workerID := req.PathValue("id")
	worker, exists := wMap.Get(workerID)

	if !exists {
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}

	resp := WorkerStatus{
		Worker: worker,
		Online: worker.IsOnline(),
	}

	err := json.NewEncoder(w).Encode(resp)
	if err != nil {
		log.Printf("Unable to encode GetWorker response to JSON. %v\n", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func (wMap *SafeWorkerMap) ListWorkers(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	wMap.mu.RLock()
	workers := slices.Collect(maps.Values(wMap.workerMap))
	wMap.mu.RUnlock() // Just need to sort and encode now, unlock

	slices.SortFunc(workers, func(a, b Worker) int {
		return cmp.Compare(a.WorkerID, b.WorkerID)
	})

	workerStatuses := []WorkerStatus{}

	for _, w := range workers {
		workerStatuses = append(workerStatuses, WorkerStatus{
			Worker: w,
			Online: w.IsOnline(),
		})
	}

	resp := ListWorkersResponse{
		Workers: workerStatuses,
	}

	err := json.NewEncoder(w).Encode(resp)
	if err != nil {
		log.Printf("Unable to encode ListWorkers response to JSON. %v\n", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func (wMap *SafeWorkerMap) RegisterWorker(w http.ResponseWriter, req *http.Request) {
	var newWorker Worker
	err := json.NewDecoder(req.Body).Decode(&newWorker)
	if err != nil {
		log.Printf("Unable to decode RegisterWorker request to JSON. %v\n", err)
		http.Error(w, "Bad Request Error", http.StatusBadRequest)
		return
	}

	if newWorker.WorkerID == "" {
		log.Printf("Reject register worker with empty WorkerID.")
		http.Error(w, "Cannot register worker with empty WorkerID", http.StatusBadRequest)
		return
	}

	newWorker.LastSeen = time.Now()

	workerExists := wMap.Put(newWorker)
	if workerExists {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
}

func (wMap *SafeWorkerMap) eligibleWorkers(requiredVRAM float64) []Worker {
	wMap.mu.RLock()
	defer wMap.mu.RUnlock()
	var eligibleWorkers []Worker

	for _, worker := range wMap.workerMap {
		if !worker.IsOnline() || !worker.ComfyUIAvailable {
			continue
		}

		if worker.VRAMFreeGiB == nil || *worker.VRAMFreeGiB < requiredVRAM {
			continue
		}

		eligibleWorkers = append(eligibleWorkers, worker)
	}

	slices.SortFunc(eligibleWorkers, func(a, b Worker) int {
		return cmp.Compare(*b.VRAMFreeGiB, *a.VRAMFreeGiB)
	})

	return eligibleWorkers
}

func (worker Worker) SendJob(job Job) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	jobJson, err := json.Marshal(job)
	if err != nil {
		log.Printf("Failed to marshal job %v to JSON. %v", job, err)
		return false
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, worker.URL+"/jobs", bytes.NewReader(jobJson))
	httpReq.Header.Set("Content-Type", "application/json")
	if err != nil {
		log.Printf("Failed to create jobs POST request with %v: %v\n", jobJson, err)
		return false
	}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		log.Printf("Failed to POST jobs for worker %v. %v\n", worker.WorkerID, err)
		return false
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Worker %v did not return good status code. Status: %v.\n", worker.WorkerID, resp.StatusCode)
		return false
	}

	var sendJobResp SendJobResponse
	err = json.NewDecoder(resp.Body).Decode(&sendJobResp)
	if err != nil {
		log.Printf("Unable to decode sendJob response from JSON. %v\n", err)
		return false
	}

	if sendJobResp.JobID != job.ID {
		log.Printf("Job ID returned from worker %v is different from job that was sent. Sent JobID: %v, recevied JobID: %v.\n", worker.WorkerID, job.ID, sendJobResp.JobID)
		return false
	}

	if sendJobResp.Status != "accepted" {
		log.Printf("Sent job %v to worker %v, but received status '%v'. Expected 'accepted'.\n", job.ID, worker.WorkerID, sendJobResp.Status)
		return false
	}

	return true
}

func (wMap *SafeWorkerMap) ScheduleWorker(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var scheduleReq ScheduleRequest
	err := json.NewDecoder(req.Body).Decode(&scheduleReq)
	if err != nil {
		log.Printf("Unable to decode Schedule request from JSON. %v\n", err)
		http.Error(w, "Bad Request Error", http.StatusBadRequest)
		return
	}

	if scheduleReq.RequiredVRAMGiB <= 0 {
		log.Println("Unable to process Schedule request with RequiredVRAMGiB <= 0.")
		http.Error(w, "RequiredVRAMGiB must be greater than 0", http.StatusBadRequest)
		return
	}

	eligibleWorkers := wMap.eligibleWorkers(scheduleReq.RequiredVRAMGiB)
	if len(eligibleWorkers) == 0 {
		log.Printf("Unable to find available worker for requested free VRAM %v", scheduleReq.RequiredVRAMGiB)
		http.Error(w, "Unable to find available worker.", http.StatusServiceUnavailable)
		return
	}

	var chosenWorker Worker
	workerAccepted := false

	job := Job{
		ID: uuid.NewString(),
	}

	for _, worker := range eligibleWorkers {
		accepted := worker.SendJob(job)
		if accepted {
			chosenWorker = worker
			workerAccepted = true
			break
		}
	}

	if !workerAccepted {
		log.Println("No eligible worker accepted job.")
		http.Error(w, "Unable to find available worker.", http.StatusServiceUnavailable)
		return
	}

	resp := ScheduleResponse{
		JobID:  job.ID,
		Worker: chosenWorker,
	}

	err = json.NewEncoder(w).Encode(resp)
	if err != nil {
		log.Printf("Unable to encode Schedule response to JSON. %v\n", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func readAndValidateEnvValues() {
	var err error
	myEnv, err = godotenv.Read()
	if err != nil {
		log.Fatalf("No .env file found. %v", err)
	}

	requiredEnvKeys := []string{"CONTROL_PLANE_PORT"}
	for _, key := range requiredEnvKeys {
		if val, ok := myEnv[key]; !ok || val == "" {
			log.Fatalf("Missing required env value for %v.", key)
		}
	}
}

func main() {
	readAndValidateEnvValues()

	safeWorkerMap := SafeWorkerMap{
		workerMap: make(map[string]Worker),
	}

	http.HandleFunc("GET /workers", safeWorkerMap.ListWorkers)
	http.HandleFunc("GET /workers/{id}", safeWorkerMap.GetWorker)
	http.HandleFunc("POST /workers/register", safeWorkerMap.RegisterWorker)
	http.HandleFunc("POST /schedule", safeWorkerMap.ScheduleWorker)

	err := http.ListenAndServe(":"+myEnv["CONTROL_PLANE_PORT"], nil)
	if err != nil {
		log.Fatalf("Failed to start server on port %v. %v", myEnv["CONTROL_PLANE_PORT"], err)
	}
}
