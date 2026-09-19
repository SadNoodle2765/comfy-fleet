package main

import (
	"cmp"
	"encoding/json"
	"io"
	"log"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
)

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
		log.Printf("Unable to encode ListWorkers response to JSON. %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
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
		log.Printf("Unable to encode GetWorker response to JSON. %v", err)
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
	jobResp, _, err := worker.GetJob(cp.httpClient, jobID)
	if err != nil {
		log.Printf("Failed to get job from worker %v, job_id %v. %v", worker.WorkerID, jobID, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	jobRecord.Status = jobResp.Status
	jobRecord.Image = jobResp.Image
	jMap.Put(jobRecord)

	err = json.NewEncoder(w).Encode(jobRecord)
	if err != nil {
		log.Printf("Unable to encode JobRecord to JSON. %v", err)
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
	imageResp, err := worker.GetImage(cp.httpClient, req.Context(), jobID)
	if err != nil {
		log.Printf("Failed to get image from worker %v, job_id %v. %v", worker.WorkerID, jobID, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	defer imageResp.Body.Close()

	w.Header().Set("Content-Type", imageResp.Header.Get("Content-Type"))
	_, err = io.Copy(w, imageResp.Body)
	if err != nil {
		log.Printf("Failed to stream image from worker %v with job_id %v. %v", worker.WorkerID, jobID, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func (cp *ControlPlane) RegisterWorker(w http.ResponseWriter, req *http.Request) {
	wMap := &cp.workerMap
	var newWorker Worker
	err := json.NewDecoder(req.Body).Decode(&newWorker)
	if err != nil {
		log.Printf("Unable to decode RegisterWorker request to JSON. %v", err)
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

func (cp *ControlPlane) ScheduleWorker(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var scheduleReq ScheduleRequest
	err := json.NewDecoder(req.Body).Decode(&scheduleReq)
	if err != nil {
		log.Printf("Unable to decode Schedule request from JSON. %v", err)
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
		log.Printf("Unable to schedule job on any eligible worker. Required VRAM: %v", scheduleReq.RequiredVRAMGiB)
		http.Error(w, "Unable to schedule job on any eligible worker.", http.StatusServiceUnavailable)
		return
	}

	job := Job{
		ID: uuid.NewString(),
		// ID:     "2ac50913-4ed8-45f7-9190-d7cd30d812345",
		Prompt: scheduleReq.Prompt,
	}

	chosenWorker, promptID, err := cp.dispatchJob(job, eligibleWorkers)
	if err != nil {
		log.Println("Failed dispatching job to workers.")
		http.Error(w, "Unable to dispatch job to worker.", http.StatusServiceUnavailable)
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
		log.Printf("Unable to encode Schedule response to JSON. %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}
