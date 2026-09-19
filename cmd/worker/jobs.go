package main

import (
	"context"
	"fmt"
	"log"
	"sync"
)

type SafeJobMap struct {
	mu   sync.RWMutex
	jobs map[string]*JobState
}

type JobState struct {
	PromptID    string
	Submitting  bool
	Done        chan struct{}
	FailureType FailureType
}

type JobSnapshot struct {
	PromptID         string
	Submitting       bool
	FailureType      FailureType
	SubmissionStatus SubmissionStatus
}

type SubmissionStatus int

const (
	SubmissionCreated     SubmissionStatus = iota // 0
	SubmissionRunning                             // 1
	SubmissionCompleted                           // 2
	SubmissionMissing                             // 3
	SubmissionCannotRetry                         // 4
)

func (s SubmissionStatus) String() string {
	switch s {
	case SubmissionCreated:
		return "created"
	case SubmissionRunning:
		return "submitting"
	case SubmissionCompleted:
		return "completed"
	case SubmissionMissing:
		return "missing"
	case SubmissionCannotRetry:
		return "cannot_retry"
	default:
		return "unknown"
	}
}

func (m *SafeJobMap) BeginSubmission(jobID string) (submissionStatus SubmissionStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if jobState, exists := m.jobs[jobID]; exists {
		if jobState.Submitting {
			log.Printf("JobID %v is currently being submitted.", jobID)
			return SubmissionRunning
		} else if jobState.FailureType == NoFailure {
			log.Printf("JobID %v has already completed submission.", jobID)
			return SubmissionCompleted
		} else if jobState.FailureType == NonRetryableFailure || jobState.FailureType == AmbiguousFailure {
			log.Printf("Previous job attempt for %v failed but cannot retry.", jobID)
			return SubmissionCannotRetry
		} else if jobState.FailureType == RetryableFailure {
			log.Printf("Previous job attempt for %v failed. Retrying by creating new jobState", jobID)
		}
	}

	log.Printf("Creating new JobState and reserving for job_id %v.\n", jobID)
	newJobState := JobState{
		Submitting:  true,
		Done:        make(chan struct{}),
		FailureType: NoFailure,
	}

	m.jobs[jobID] = &newJobState
	return SubmissionCreated
}

func (m *SafeJobMap) CompleteSubmission(jobID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	jobState, exists := m.jobs[jobID]
	if !exists {
		log.Printf("Tried to complete submission for job %v that does not exist.\n", jobID)
		return
	}
	if !jobState.Submitting {
		log.Printf("Tried to complete submission for job %v that's already past completed.\n", jobID)
		return
	}
	jobState.Submitting = false
	close(jobState.Done)
}

func (m *SafeJobMap) FailSubmission(jobID string, failureType FailureType) {
	m.mu.Lock()
	defer m.mu.Unlock()

	jobState, exists := m.jobs[jobID]
	if !exists {
		log.Printf("Tried to fail and clear job %v that does not exist.\n", jobID)
		return
	}
	if jobState.Submitting {
		close(jobState.Done)
	}

	jobState.Submitting = false
	jobState.FailureType = failureType
}

func (m *SafeJobMap) WaitForJobSnapshot(jobID string, ctx context.Context) (jobSnapshot JobSnapshot, reqClosed bool) {
	for {
		m.mu.RLock()
		jobState, exists := m.jobs[jobID]
		if !exists {
			m.mu.RUnlock()
			return JobSnapshot{
				SubmissionStatus: SubmissionMissing,
			}, false
		}
		doneCh := jobState.Done
		jobSnapshot = JobSnapshot{
			PromptID:    jobState.PromptID,
			Submitting:  jobState.Submitting,
			FailureType: jobState.FailureType,
		}
		m.mu.RUnlock()

		if !jobSnapshot.Submitting {
			log.Printf("Job %v has already completed submission.", jobID)
			jobSnapshot.SubmissionStatus = SubmissionCompleted
			return jobSnapshot, false
		} else {
			log.Printf("Job %v is currently being submitted.", jobID)
			select {
			case <-ctx.Done():
				log.Println("HTTP request was closed or timed out.")
				return JobSnapshot{}, true
			case <-doneCh:
				log.Printf("Submission attempt for job_id %v was finished.\n", jobID)
			}
		}
	}
}

// JobState entry with given job_id as the key must already be in the jobs map
// Should be created immediately on receiving POST /jobs request
func (m *SafeJobMap) SetPromptID(jobID string, promptID string) (err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	jobState, exists := m.jobs[jobID]
	if !exists {
		return fmt.Errorf("invariant failed: Expected jobState with key %v to be in the jobs map but did not exist.\n", jobID)
	}

	jobState.PromptID = promptID
	return nil
}
