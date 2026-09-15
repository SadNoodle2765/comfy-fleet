package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	ID     string         `json:"job_id"`
	Prompt map[string]any `json:"prompt"`
}

type JobRecord struct {
	JobID     string         `json:"job_id"`
	PromptID  string         `json:"prompt_id"`
	WorkerID  string         `json:"worker_id"`
	Status    string         `json:"status"`
	CreatedAt time.Time      `json:"created_at"`
	Image     *ImageMetadata `json:"image"`
}

type SafeJobRecordMap struct {
	mu           sync.RWMutex
	jobRecordMap map[string]JobRecord
}

type ControlPlane struct {
	workerMap    SafeWorkerMap
	jobRecordMap SafeJobRecordMap
}

type ListWorkersResponse struct {
	Workers []WorkerStatus `json:"workers"`
}

type ScheduleRequest struct {
	RequiredVRAMGiB float64        `json:"required_vram_gib"`
	Prompt          map[string]any `json:"prompt"`
}

type ScheduleResponse struct {
	JobID    string `json:"job_id"`
	PromptID string `json:"prompt_id"`
	Worker   Worker `json:"worker"`
}

type SendJobResponse struct {
	JobID    string `json:"job_id"`
	PromptID string `json:"prompt_id"`
	Status   string `json:"status"`
}

type GetJobResponse struct {
	JobID  string         `json:"job_id"`
	Status string         `json:"status"`
	Image  *ImageMetadata `json:"image"`
}

type ImageMetadata struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

func (w Worker) IsOnline() bool {
	return time.Since(w.LastSeen) < offlineTime
}

func (w Worker) GetJob(jobID string) (jobResp GetJobResponse, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, w.URL+"/jobs/"+jobID, nil)
	if err != nil {
		log.Printf("Failed to create jobs GET request with %v: %v\n", jobID, err)
		return jobResp, fmt.Errorf("failed to create jobs GET request with %v: %w", jobID, err)
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		log.Printf("Failed to GET jobs for worker %v, job_id %v. %v\n", w.WorkerID, jobID, err)
		return jobResp, fmt.Errorf("failed to GET jobs for worker %v, job_id %v. %w", w.WorkerID, jobID, err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Worker %v did not return good status code. Status: %v.\n", w.WorkerID, resp.StatusCode)
		return jobResp, fmt.Errorf("worker %v did not return good status code. Status: %v.\n", w.WorkerID, resp.StatusCode)
	}

	if err = json.NewDecoder(resp.Body).Decode(&jobResp); err != nil {
		log.Printf("Failed to decode GetJob response. %v\n", err)
		return jobResp, fmt.Errorf("failed to decode GetJob response: %w", err)
	}

	return jobResp, nil
}

func (w Worker) GetImage(ctx context.Context, jobID string) (resp *http.Response, err error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, w.URL+"/jobs/"+jobID+"/image", nil)
	if err != nil {
		return resp, fmt.Errorf("failed to create image GET request with %v: %w", jobID, err)
	}

	resp, err = httpClient.Do(httpReq)
	if err != nil {
		return resp, fmt.Errorf("failed to GET image for worker %v, job_id %v. %w", w.WorkerID, jobID, err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return resp, fmt.Errorf("worker %v did not return good status code. Status: %v.", w.WorkerID, resp.StatusCode)
	}

	return resp, nil
}

func (jMap *SafeJobRecordMap) Get(jobID string) (JobRecord, bool) {
	jMap.mu.RLock()
	defer jMap.mu.RUnlock()

	jobRecord, exists := jMap.jobRecordMap[jobID]
	if !exists {
		return JobRecord{}, false
	}
	return jobRecord, true
}

func (jMap *SafeJobRecordMap) Put(jobRecord JobRecord) (recordExists bool) {
	jMap.mu.Lock()
	defer jMap.mu.Unlock()

	_, recordExists = jMap.jobRecordMap[jobRecord.JobID]
	jMap.jobRecordMap[jobRecord.JobID] = jobRecord

	return recordExists
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

func (cp *ControlPlane) GetWorker(w http.ResponseWriter, req *http.Request) {
	wMap := &cp.workerMap
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

func (cp *ControlPlane) ListWorkers(w http.ResponseWriter, req *http.Request) {
	wMap := &cp.workerMap
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

func (cp *ControlPlane) RegisterWorker(w http.ResponseWriter, req *http.Request) {
	wMap := &cp.workerMap
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

func (cp *ControlPlane) eligibleWorkers(requiredVRAM float64) []Worker {
	wMap := &cp.workerMap
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

// Returns PromptID
func (worker Worker) SendJob(job Job) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	jobJson, err := json.Marshal(job)
	if err != nil {
		log.Printf("Failed to marshal job %v to JSON. %v", job, err)
		return ""
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, worker.URL+"/jobs", bytes.NewReader(jobJson))
	if err != nil {
		log.Printf("Failed to create jobs POST request with %v: %v\n", jobJson, err)
		return ""
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		log.Printf("Failed to POST jobs for worker %v. %v\n", worker.WorkerID, err)
		return ""
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Worker %v did not return good status code. Status: %v.\n", worker.WorkerID, resp.StatusCode)
		return ""
	}

	var sendJobResp SendJobResponse
	err = json.NewDecoder(resp.Body).Decode(&sendJobResp)
	if err != nil {
		log.Printf("Unable to decode sendJob response from JSON. %v\n", err)
		return ""
	}

	if sendJobResp.JobID != job.ID {
		log.Printf("Job ID returned from worker %v is different from job that was sent. Sent JobID: %v, recevied JobID: %v.\n", worker.WorkerID, job.ID, sendJobResp.JobID)
		return ""
	}

	if sendJobResp.Status != "accepted" {
		log.Printf("Sent job %v to worker %v, but received status '%v'. Expected 'accepted'.\n", job.ID, worker.WorkerID, sendJobResp.Status)
		return ""
	}

	return sendJobResp.PromptID
}

func (cp *ControlPlane) ScheduleWorker(w http.ResponseWriter, req *http.Request) {
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

	if len(scheduleReq.Prompt) == 0 {
		log.Println("Unable to process Schedule request with empty prompt.")
		http.Error(w, "Prompt must not be empty", http.StatusBadRequest)
		return
	}

	eligibleWorkers := cp.eligibleWorkers(scheduleReq.RequiredVRAMGiB)
	if len(eligibleWorkers) == 0 {
		log.Printf("Unable to find available worker for requested free VRAM %v", scheduleReq.RequiredVRAMGiB)
		http.Error(w, "Unable to find available worker.", http.StatusServiceUnavailable)
		return
	}

	var chosenWorker Worker
	promptID := ""

	job := Job{
		ID:     uuid.NewString(),
		Prompt: scheduleReq.Prompt,
	}

	for _, worker := range eligibleWorkers {
		promptID = worker.SendJob(job)
		if promptID != "" {
			chosenWorker = worker
			break
		}
	}

	if promptID == "" {
		log.Println("No eligible worker accepted job.")
		http.Error(w, "Unable to find available worker.", http.StatusServiceUnavailable)
		return
	}

	jobRecord := JobRecord{
		JobID:     job.ID,
		PromptID:  promptID,
		WorkerID:  chosenWorker.WorkerID,
		Status:    "accepted",
		CreatedAt: time.Now(),
	}

	cp.jobRecordMap.Put(jobRecord)

	resp := ScheduleResponse{
		JobID:    job.ID,
		PromptID: promptID,
		Worker:   chosenWorker,
	}

	err = json.NewEncoder(w).Encode(resp)
	if err != nil {
		log.Printf("Unable to encode Schedule response to JSON. %v\n", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func (cp *ControlPlane) GetJobRecord(w http.ResponseWriter, req *http.Request) {
	wMap := &cp.workerMap
	jMap := &cp.jobRecordMap
	w.Header().Set("Content-Type", "application/json")

	jobID := req.PathValue("id")
	jobRecord, exists := jMap.Get(jobID)

	if !exists {
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}

	worker, exists := wMap.Get(jobRecord.WorkerID)
	if !exists {
		log.Printf("JobRecord for job_id %v exists, but no corresponding worker found.", jobID)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	jobResp, err := worker.GetJob(jobID)
	if err != nil {
		log.Printf("Failed to get job from worker %v, job_id %v. %v\n", worker.WorkerID, jobID, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	jobRecord.Status = jobResp.Status
	jobRecord.Image = jobResp.Image
	jMap.Put(jobRecord)

	err = json.NewEncoder(w).Encode(jobRecord)
	if err != nil {
		log.Printf("Unable to encode JobRecord to JSON. %v\n", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func (cp *ControlPlane) GetJobImage(w http.ResponseWriter, req *http.Request) {
	wMap := &cp.workerMap
	jMap := &cp.jobRecordMap

	jobID := req.PathValue("id")
	jobRecord, exists := jMap.Get(jobID)

	if !exists {
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}

	worker, exists := wMap.Get(jobRecord.WorkerID)
	if !exists {
		log.Printf("JobRecord for job_id %v exists, but no corresponding worker found.", jobID)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	imageResp, err := worker.GetImage(req.Context(), jobID)
	if err != nil {
		log.Printf("Failed to get image from worker %v, job_id %v. %v\n", worker.WorkerID, jobID, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	defer imageResp.Body.Close()

	w.Header().Set("Content-Type", imageResp.Header.Get("Content-Type"))
	_, err = io.Copy(w, imageResp.Body)
	if err != nil {
		log.Printf("Failed to stream image from worker %v with job_id %v. %v\n", worker.WorkerID, jobID, err)
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

	controlPlane := ControlPlane{
		workerMap: SafeWorkerMap{
			workerMap: make(map[string]Worker),
		},
		jobRecordMap: SafeJobRecordMap{
			jobRecordMap: make(map[string]JobRecord),
		},
	}

	http.HandleFunc("GET /workers", controlPlane.ListWorkers)
	http.HandleFunc("GET /workers/{id}", controlPlane.GetWorker)
	http.HandleFunc("GET /jobs/{id}", controlPlane.GetJobRecord)
	http.HandleFunc("GET /jobs/{id}/image", controlPlane.GetJobImage)
	http.HandleFunc("POST /workers/register", controlPlane.RegisterWorker)
	http.HandleFunc("POST /schedule", controlPlane.ScheduleWorker)

	err := http.ListenAndServe(":"+myEnv["CONTROL_PLANE_PORT"], nil)
	if err != nil {
		log.Fatalf("Failed to start server on port %v. %v", myEnv["CONTROL_PLANE_PORT"], err)
	}
}
