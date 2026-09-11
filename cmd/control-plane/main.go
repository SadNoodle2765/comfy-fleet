package main

import (
	"cmp"
	"encoding/json"
	"log"
	"maps"
	"net/http"
	"slices"
	"sync"
)

type Worker struct {
	WorkerID         string   `json:"worker_id"`
	GPU              *string  `json:"gpu"`
	VRAMGiB          *float64 `json:"vram_gib"`
	VRAMFreeGiB      *float64 `json:"vram_free_gib"`
	ComfyUIAvailable bool     `json:"comfyui_available"`
}

type SafeWorkerMap struct {
	mu        sync.RWMutex
	workerMap map[string]Worker
}

type GetWorkerResponse struct {
	Worker Worker `json:"worker"`
}

type ListWorkersResponse struct {
	Workers []Worker `json:"workers"`
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

	resp := GetWorkerResponse{
		Worker: worker,
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
	resp := ListWorkersResponse{
		Workers: workers,
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

	workerExists := wMap.Put(newWorker)
	if workerExists {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
}

func main() {
	safeWorkerMap := SafeWorkerMap{
		workerMap: make(map[string]Worker),
	}

	http.HandleFunc("GET /workers", safeWorkerMap.ListWorkers)
	http.HandleFunc("GET /workers/{id}", safeWorkerMap.GetWorker)
	http.HandleFunc("POST /workers/register", safeWorkerMap.RegisterWorker)

	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatalf("Failed to start server on port 8080. %v", err)
	}
}
