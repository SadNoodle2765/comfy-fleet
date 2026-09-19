package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

type SafeWorkerMap struct {
	mu        sync.RWMutex
	workerMap map[string]Worker
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

func (w Worker) IsOnline() bool {
	return time.Since(w.LastSeen) < offlineTime
}

func (w Worker) GetJob(httpClient HTTPDoer, jobID string) (jobResp GetJobResponse, getJobStatus GetJobStatus, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, w.URL+"/jobs/"+jobID, nil)
	if err != nil {
		log.Printf("Failed to create jobs GET request with %v: %v\n", jobID, err)
		return jobResp, GetJobUnknown, fmt.Errorf("failed to create jobs GET request with %v: %w", jobID, err)
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		log.Printf("Failed to GET jobs for worker %v, job_id %v. %v\n", w.WorkerID, jobID, err)
		return jobResp, GetJobUnknown, fmt.Errorf("failed to GET jobs for worker %v, job_id %v. %w", w.WorkerID, jobID, err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Worker %v did not return good status code. Status: %v.", w.WorkerID, resp.StatusCode)
		if resp.StatusCode == http.StatusNotFound {
			log.Printf("Worker %v did not receive job %v. Status: %v.", w.WorkerID, jobID, resp.StatusCode)
			return jobResp, GetJobNotFound, fmt.Errorf("worker %v did not return good status code. Status: %v.", w.WorkerID, resp.StatusCode)
		}
		return jobResp, GetJobUnknown, fmt.Errorf("worker %v did not return good status code. Status: %v.", w.WorkerID, resp.StatusCode)
	}

	if err = json.NewDecoder(resp.Body).Decode(&jobResp); err != nil {
		log.Printf("Failed to decode GetJob response. %v\n", err)
		return jobResp, GetJobUnknown, fmt.Errorf("failed to decode GetJob response: %w", err)
	}

	return jobResp, GetJobFound, nil
}

func (w Worker) GetImage(httpClient HTTPDoer, ctx context.Context, jobID string) (resp *http.Response, err error) {
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
