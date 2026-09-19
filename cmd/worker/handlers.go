package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

func handleJSONErrorInternal(w http.ResponseWriter, err error) {
	log.Printf("Error while handling JSON: %s\n", err)
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}

func handleJSONErrorExternal(w http.ResponseWriter, err error) {
	log.Printf("Error while handling JSON: %s\n", err)
	http.Error(w, "Bad Request Error", http.StatusBadRequest)
}

func health(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	goodHealth := HealthResponse{
		Status: "ok",
	}

	err := json.NewEncoder(w).Encode(goodHealth)
	if err != nil {
		handleJSONErrorInternal(w, err)
		return
	}
}

func capabilities(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	systemStats := getComfyUISystemStats()
	capabilitiesResponse := systemStats.toCapabilities()

	err := json.NewEncoder(w).Encode(capabilitiesResponse)
	if err != nil {
		handleJSONErrorInternal(w, err)
		return
	}
}

func (worker *Worker) receiveJob(w http.ResponseWriter, req *http.Request) {
	jobMap := &worker.jobMap
	w.Header().Set("Content-Type", "application/json")

	var jobRequest ReceiveJobRequest
	err := json.NewDecoder(req.Body).Decode(&jobRequest)
	if err != nil {
		handleJSONErrorExternal(w, err)
		return
	}

	jobID := jobRequest.JobID
	if jobID == "" {
		log.Println("Reject job request with empty job ID.")
		http.Error(w, "Cannot process job with empty job ID.", http.StatusBadRequest)
		return
	}

	if len(jobRequest.Prompt) == 0 {
		log.Println("Reject job request with empty prompt.")
		http.Error(w, "Cannot process job with empty prompt.", http.StatusBadRequest)
		return
	}

	for {
		submissionStatus := jobMap.BeginSubmission(jobID)
		if submissionStatus == SubmissionCannotRetry {
			log.Printf("Previous receive job for %v attempt failed and cannot retry.", jobID)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		if submissionStatus == SubmissionCreated {
			log.Printf("Worker took charge of job %v.\n", jobID)
			break
		}
		if submissionStatus == SubmissionRunning || submissionStatus == SubmissionCompleted {
			jobSnapshot, reqClosed := jobMap.WaitForJobSnapshot(jobID, req.Context())
			if reqClosed {
				log.Printf("HTTP request was closed or timed out.")
				return
			}
			switch jobSnapshot.FailureType {
			case RetryableFailure:
				log.Printf("Job %v failed with retryable error. Retrying.\n", jobID)
				continue
			case NonRetryableFailure:
				log.Printf("Job %v failed with non-retryable error.\n", jobID)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			case AmbiguousFailure:
				log.Printf("Job %v failed with ambiguous error.\n", jobID)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}

			if jobSnapshot.PromptID == "" {
				log.Printf("Job %v has already completed submission, but prompt_id is empty.", jobID)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}

			jobResponse := ReceiveJobResponse{
				JobID:    jobID,
				PromptID: jobSnapshot.PromptID,
				Status:   "accepted",
			}
			err = json.NewEncoder(w).Encode(jobResponse)
			if err != nil {
				handleJSONErrorInternal(w, err)
				return
			}
			return
		}
	}

	// Job does not exist and reservation is created
	promptResponse, failureType, err := sendPromptToComfyUI(jobRequest.Prompt)
	if err != nil {
		log.Printf("Failed sending prompt to ComfyUI. %v\n", err)
		http.Error(w, "Failed sending prompt to ComfyUI.", http.StatusInternalServerError)
		jobMap.FailSubmission(jobID, failureType)
		return
	}

	if err = jobMap.SetPromptID(jobID, promptResponse.PromptID); err != nil {
		log.Printf("Failed to set promptID for job %v. %v\n", jobID, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		jobMap.FailSubmission(jobID, AmbiguousFailure)
		return
	}
	jobMap.CompleteSubmission(jobID)

	jobResponse := ReceiveJobResponse{
		JobID:    jobID,
		PromptID: promptResponse.PromptID,
		Status:   "accepted",
	}

	err = json.NewEncoder(w).Encode(jobResponse)
	if err != nil {
		handleJSONErrorInternal(w, err)
		return
	}
}

func (worker *Worker) getJob(w http.ResponseWriter, req *http.Request) {
	jobMap := &worker.jobMap
	w.Header().Set("Content-Type", "application/json")

	jobID := req.PathValue("id")

	if jobID == "" {
		log.Println("Unable to process GetJobRequest with empty job_id.")
		http.Error(w, "Cannot process GetJobRequest with empty job_id.", http.StatusBadRequest)
		return
	}

	jobSnapshot, reqClosed := jobMap.WaitForJobSnapshot(jobID, req.Context())
	if reqClosed {
		return
	}
	if jobSnapshot.SubmissionStatus == SubmissionMissing {
		log.Printf("Cannot find existing job with job_id %v.\n", jobID)
		http.Error(w, fmt.Sprintf("Cannot find existing job with job_id %v.", jobID), http.StatusNotFound)
		return
	}
	if jobSnapshot.FailureType != NoFailure {
		log.Printf("Job %v previously failed with %v.\n", jobID, jobSnapshot.FailureType)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	promptID := jobSnapshot.PromptID

	var imagePointer *ComfyImage
	promptStatus := getPromptStatusFromComfyUI(promptID)
	if promptStatus == StatusCompleted {
		image, err := getImageMetadataFromComfyUI(promptID)
		if err != nil {
			log.Printf("Failed to get image metadata with completed status for prompt_id %v. %v", promptID, err)
			http.Error(w, fmt.Sprintf("Failed getting image metadata for job_id %v with completed status.", jobID), http.StatusInternalServerError)
			return
		}
		imagePointer = &image
	}

	resp := GetJobResponse{
		JobID:    jobID,
		PromptID: promptID,
		Status:   promptStatus.String(),
		Image:    imagePointer,
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("Failed encoding GetJobResponse %v. %v\n", resp, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func (worker *Worker) getImage(w http.ResponseWriter, req *http.Request) {
	jobMap := &worker.jobMap
	jobID := req.PathValue("id")

	if jobID == "" {
		log.Println("Unable to process GetImageRequest with empty job_id.")
		http.Error(w, "Cannot process GetImageRequest with empty job_id.", http.StatusBadRequest)
		return
	}

	jobSnapshot, reqClosed := jobMap.WaitForJobSnapshot(jobID, req.Context())
	if reqClosed {
		return
	}
	if jobSnapshot.SubmissionStatus == SubmissionMissing {
		log.Printf("Cannot find existing job with job_id %v.\n", jobID)
		http.Error(w, fmt.Sprintf("Cannot find existing job with job_id %v.", jobID), http.StatusNotFound)
		return
	}
	if jobSnapshot.FailureType != NoFailure {
		log.Printf("Job %v previously failed with %v.\n", jobID, jobSnapshot.FailureType)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
	defer cancel()

	promptID := jobSnapshot.PromptID

	resp, err := getImageFromComfyUI(ctx, promptID)

	if err != nil {
		log.Printf("Failed getting image from ComfyUI for prompt_id %v. %v", promptID, err)
		http.Error(w, "Failed getting image from ComfyUI.", http.StatusInternalServerError)
		return
	}

	defer resp.Body.Close()

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		log.Printf("Failed to stream image for prompt_id %v. %v", promptID, err)
		http.Error(w, "Failed to stream image from ComfyUI.", http.StatusInternalServerError)
		return
	}
}
