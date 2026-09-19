package main

import (
	"net/http"
	"time"
)

type FailureType int

const (
	NoFailure           FailureType = iota // 0
	RetryableFailure                       // 1
	NonRetryableFailure                    // 2
	AmbiguousFailure                       // 3
)

func (s FailureType) String() string {
	switch s {
	case NoFailure:
		return "no failure"
	case RetryableFailure:
		return "retryable failure"
	case NonRetryableFailure:
		return "non-retryable failure"
	case AmbiguousFailure:
		return "ambiguous failure"
	default:
		return "unknown failure"
	}
}

type GetJobStatus int

const (
	GetJobFound GetJobStatus = iota
	GetJobNotFound
	GetJobUnknown
)

func (s GetJobStatus) String() string {
	switch s {
	case GetJobFound:
		return "get job found"
	case GetJobNotFound:
		return "get job not found"
	case GetJobUnknown:
		return "get job unknown"
	}
	return "unknown get job status"
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

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
	JobID    string         `json:"job_id"`
	PromptID string         `json:"prompt_id"`
	Status   string         `json:"status"`
	Image    *ImageMetadata `json:"image"`
}

type ImageMetadata struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}
