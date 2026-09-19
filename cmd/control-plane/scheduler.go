package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"syscall"
	"time"
)

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

func (cp *ControlPlane) dispatchJob(
	job Job,
	eligibleWorkers []Worker,
) (chosenWorker Worker, promptID string, err error) {
	for _, worker := range eligibleWorkers {
	RetryWorkerLoop:
		for attempt := range 2 {
			attemptPromptID, failureType, err := worker.SendJob(cp.httpClient, job)
			switch failureType {
			case NoFailure:
				log.Printf("worker %v accepted job %v and gave prompt_id %v.", worker.WorkerID, job.ID, attemptPromptID)
				return worker, attemptPromptID, nil
			case RetryableFailure:
				if attempt == 0 {
					log.Printf("failed sending job %v to worker %v. retryable failure so try and find new worker. %v", job.ID, worker.WorkerID, err)
					break RetryWorkerLoop
				}
				log.Printf("failed sending job %v to worker %v. retryable failure but initially failed ambiguously so won't find new worker. %v", job.ID, worker.WorkerID, err)
				return Worker{}, "", err
			case NonRetryableFailure:
				log.Printf("failed sending job %v to worker %v. non-retryable failure so don't find new worker. %v", job.ID, worker.WorkerID, err)
				return Worker{}, "", err
			case AmbiguousFailure:
				log.Printf("failed sending job %v to worker %v. ambiguous failure so check if job went through. %v", job.ID, worker.WorkerID, err)
				jobResp, getJobStatus, err := worker.GetJob(cp.httpClient, job.ID)
				switch getJobStatus {
				case GetJobFound:
					log.Printf("worker %v actually accepted job %v.", worker.WorkerID, job.ID)
					return worker, jobResp.PromptID, nil
				case GetJobNotFound:
					if attempt == 0 {
						log.Printf("worker %v never received job %v. Retrying same worker.", worker.WorkerID, job.ID)
						continue
					}
					log.Printf("Worker %v still reports job %v missing after second ambiguous POST. Stopping because submission remains uncertain.", worker.WorkerID, job.ID)
					return Worker{}, "", err
				case GetJobUnknown:
					log.Printf("unknown if worker %v received job %v. Not trying new worker. %v", worker.WorkerID, job.ID, err)
					return Worker{}, "", err
				}
			}
		}
	}

	return Worker{}, "", errors.New("unable to find worker to accept job.")
}

func (worker Worker) SendJob(httpClient HTTPDoer, job Job) (promptID string, failureType FailureType, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	jobJson, err := json.Marshal(job)
	if err != nil {
		log.Printf("Failed to marshal job %v to JSON. %v", job, err)
		return "", NonRetryableFailure, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, worker.URL+"/jobs", bytes.NewReader(jobJson))
	if err != nil {
		log.Printf("Failed to create jobs POST request with %v: %v\n", jobJson, err)
		return "", NonRetryableFailure, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		log.Printf("Failed to POST jobs for worker %v. %v\n", worker.WorkerID, err)
		if errors.Is(err, syscall.ECONNREFUSED) {
			return "", RetryableFailure, err
		}
		return "", AmbiguousFailure, err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Worker %v did not return good status code. Status: %v.\n", worker.WorkerID, resp.StatusCode)
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			log.Printf("failed to read worker error response: %v", readErr)
		}
		err := fmt.Errorf(
			"Worker returned status %d. Response Body: %s.",
			resp.StatusCode,
			string(body),
		)
		switch resp.StatusCode {
		case http.StatusBadRequest:
			return "", NonRetryableFailure, err
		case http.StatusInternalServerError:
			return "", AmbiguousFailure, err
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return "", AmbiguousFailure, err
		default:
			return "", AmbiguousFailure, err
		}
	}

	var sendJobResp SendJobResponse
	err = json.NewDecoder(resp.Body).Decode(&sendJobResp)
	if err != nil {
		log.Printf("Unable to decode sendJob response from JSON. %v\n", err)
		return "", AmbiguousFailure, err
	}

	if sendJobResp.JobID != job.ID {
		log.Printf("Job ID returned from worker %v is different from job that was sent. Sent JobID: %v, recevied JobID: %v.\n", worker.WorkerID, job.ID, sendJobResp.JobID)
		return "", AmbiguousFailure, fmt.Errorf(
			"worker returned job_id %q. expected %q.",
			sendJobResp.JobID,
			job.ID,
		)
	}

	if sendJobResp.Status != "accepted" {
		log.Printf("Sent job %v to worker %v, but received status '%v'. Expected 'accepted'.\n", job.ID, worker.WorkerID, sendJobResp.Status)
		return "", AmbiguousFailure, fmt.Errorf(
			`worker returned status %q. expected "accepted".`,
			sendJobResp.Status,
		)
	}

	if sendJobResp.PromptID == "" {
		log.Printf("Sent job %v to worker %v, but received empty promptID.", job.ID, worker.WorkerID)
		return "", AmbiguousFailure, errors.New("worker returned empty promptID.")
	}

	return sendJobResp.PromptID, NoFailure, nil
}
