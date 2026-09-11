package main

import (
	"cmp"
	"encoding/json"
	"log"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

var myEnv map[string]string

const offlineTime = 30 * time.Second

type Worker struct {
	WorkerID         string    `json:"worker_id"`
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

type ListWorkersResponse struct {
	Workers []WorkerStatus `json:"workers"`
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

	err := http.ListenAndServe(":"+myEnv["CONTROL_PLANE_PORT"], nil)
	if err != nil {
		log.Fatalf("Failed to start server on port %v. %v", myEnv["CONTROL_PLANE_PORT"], err)
	}
}
