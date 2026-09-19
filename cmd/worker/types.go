package main

type Worker struct {
	jobMap SafeJobMap
}

type PromptStatus int

const (
	StatusCompleted PromptStatus = iota // 0
	StatusAccepted                      // 1
	StatusRunning                       // 2
	StatusPending                       // 3
	StatusUnknown                       // 4
)

// String implements the fmt.Stringer interface
func (s PromptStatus) String() string {
	switch s {
	case StatusCompleted:
		return "completed"
	case StatusAccepted:
		return "accepted"
	case StatusRunning:
		return "running"
	case StatusPending:
		return "pending"
	default:
		return "unknown"
	}
}

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

type HistoryResponse map[string]HistoryEntry

type HistoryEntry struct {
	Outputs map[string]OutputNode `json:"outputs"`
	Status  HistoryStatus         `json:"status"`
}

type OutputNode struct {
	Images []ComfyImage `json:"images"`
}

type ComfyImage struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

type HistoryStatus struct {
	StatusStr string `json:"status_str"`
	Completed bool   `json:"completed"`
}

type QueueObject [][]any

type QueueResponse struct {
	QueueRunning QueueObject `json:"queue_running"`
	QueuePending QueueObject `json:"queue_pending"`
}

type HealthResponse struct {
	Status string `json:"status"`
}

type WorkerCapabilities struct {
	WorkerID         string   `json:"worker_id"`
	URL              string   `json:"url"`
	GPU              *string  `json:"gpu"`
	VRAMGiB          *float64 `json:"vram_gib"`
	VRAMFreeGiB      *float64 `json:"vram_free_gib"`
	ComfyUIAvailable bool     `json:"comfyui_available"`
}

type ReceiveJobRequest struct {
	JobID  string         `json:"job_id"`
	Prompt map[string]any `json:"prompt"`
}

type ReceiveJobResponse struct {
	JobID    string `json:"job_id"`
	PromptID string `json:"prompt_id"`
	Status   string `json:"status"`
}

type PromptComfyUIRequest struct {
	Prompt map[string]any `json:"prompt"`
}

type PromptComfyUIResponse struct {
	PromptID   string         `json:"prompt_id"`
	Number     int            `json:"number"`
	NodeErrors map[string]any `json:"node_errors"`
}

type GetJobResponse struct {
	JobID    string      `json:"job_id"`
	PromptID string      `json:"prompt_id"`
	Status   string      `json:"status"`
	Image    *ComfyImage `json:"image"`
}

type ComfyUISystemStats struct {
	Devices []Device `json:"devices"`
}

type Device struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Index     int    `json:"index"`
	VRAMTotal int    `json:"vram_total"`
	VRAMFree  int    `json:"vram_free"`
}
