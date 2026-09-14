package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

var myEnv map[string]string

const bytesPerGiB = (1 << 30)

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
}

type Worker struct {
	jobToPromptMap SafeJobToPromptMap
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

type SafeJobToPromptMap struct {
	mu     sync.RWMutex
	j2pMap map[string]string
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

type GetJobStatusResponse struct {
	Status string `json:"status"`
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

func bytesToGiB(bytes int) float64 {
	gib := float64(bytes) / bytesPerGiB
	return math.Round(gib*100) / 100
}

func handleJSONErrorInternal(w http.ResponseWriter, err error) {
	log.Printf("Error while handling JSON: %s\n", err)
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}

func handleJSONErrorExternal(w http.ResponseWriter, err error) {
	log.Printf("Error while handling JSON: %s\n", err)
	http.Error(w, "Bad Request Error", http.StatusBadRequest)
}

func (m *SafeJobToPromptMap) Get(jobID string) (promptID string, exists bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	promptID, exists = m.j2pMap[jobID]
	return promptID, exists
}

func (m *SafeJobToPromptMap) Put(jobID string, promptID string) (exists bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, exists = m.j2pMap[jobID]
	m.j2pMap[jobID] = promptID

	return exists
}

func registerToControlPlane() {
	cap := getComfyUISystemStats().toCapabilities()

	reqJson, err := json.Marshal(cap)
	if err != nil {
		log.Printf("Failed to marshal register worker request to JSON. %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, myEnv["CONTROL_PLANE_URL"]+"/workers/register", bytes.NewReader(reqJson))
	if err != nil {
		log.Printf("Failed to create register worker POST request: %v\n", err)
		return
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		log.Printf("Failed to register worker to control plane. %v", err)
		return
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Failed to read response body: %v", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		log.Printf("Worker %v updated successfully.\n", myEnv["WORKER_ID"])
	case http.StatusCreated:
		log.Printf("Worker %v registered successfully.\n", myEnv["WORKER_ID"])
	default:
		log.Printf("Worker %v registration failed. Status: %v. %s",
			myEnv["WORKER_ID"],
			resp.StatusCode,
			body,
		)
	}
}

func registerToControlPlaneHeartbeat() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	registerToControlPlane()
	log.Println("Initial register worker call.")

	for {
		t := <-ticker.C
		log.Printf("Register worker heartbeat tick at %v.\n", t)
		registerToControlPlane()
	}
}

func getComfyUISystemStats() *ComfyUISystemStats {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, myEnv["COMFYUI_URL"]+"/system_stats", nil)
	if err != nil {
		log.Printf("Failed to create system_stats GET request: %v\n", err)
		return nil
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		log.Printf("Could not connect to ComfyUI. %v\n", err)
		return nil
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("API returned bad status code. %d\n", resp.StatusCode)
		return nil
	}

	var systemStats ComfyUISystemStats
	err = json.NewDecoder(resp.Body).Decode(&systemStats)
	if err != nil {
		log.Printf("Unable to parse system stats returned by ComfyUI. %v\n", err)
		return nil
	}

	return &systemStats
}

func (systemStats *ComfyUISystemStats) toCapabilities() WorkerCapabilities {
	resp := WorkerCapabilities{
		WorkerID:         myEnv["WORKER_ID"],
		URL:              myEnv["WORKER_URL"],
		GPU:              nil,
		VRAMGiB:          nil,
		VRAMFreeGiB:      nil,
		ComfyUIAvailable: systemStats != nil,
	}

	if systemStats != nil && len(systemStats.Devices) > 0 {
		vramTotalGiB := bytesToGiB(systemStats.Devices[0].VRAMTotal)
		vramFreeGiB := bytesToGiB(systemStats.Devices[0].VRAMFree)
		resp.GPU = &systemStats.Devices[0].Name
		resp.VRAMGiB = &vramTotalGiB
		resp.VRAMFreeGiB = &vramFreeGiB
	}

	return resp
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
	w.Header().Set("Content-Type", "application/json")

	var jobRequest ReceiveJobRequest
	err := json.NewDecoder(req.Body).Decode(&jobRequest)
	if err != nil {
		handleJSONErrorExternal(w, err)
		return
	}
	if jobRequest.JobID == "" {
		log.Println("Reject job request with empty job ID.")
		http.Error(w, "Cannot process job with empty job ID.", http.StatusBadRequest)
		return
	}

	if len(jobRequest.Prompt) == 0 {
		log.Println("Reject job request with empty prompt.")
		http.Error(w, "Cannot process job with empty prompt.", http.StatusBadRequest)
		return
	}

	promptResponse, err := sendPromptToComfyUI(jobRequest.Prompt)
	if err != nil {
		log.Printf("Failed sending prompt to ComfyUI. %v\n", err)
		http.Error(w, "Failed sending prompt to ComfyUI.", http.StatusInternalServerError)
		return
	}

	if promptResponse.PromptID == "" {
		log.Printf("Got empty prompt_id from ComfyUI. %v\n", err)
		http.Error(w, "Got empty prompt_id from ComfyUI.", http.StatusInternalServerError)
		return
	}

	j2pMap := &worker.jobToPromptMap
	j2pMap.Put(jobRequest.JobID, promptResponse.PromptID)

	jobResponse := ReceiveJobResponse{
		JobID:    jobRequest.JobID,
		PromptID: promptResponse.PromptID,
		Status:   "accepted",
	}

	err = json.NewEncoder(w).Encode(jobResponse)
	if err != nil {
		handleJSONErrorInternal(w, err)
		return
	}
}

func sendPromptToComfyUI(prompt map[string]any) (PromptComfyUIResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	reqBody := PromptComfyUIRequest{
		Prompt: prompt,
	}

	reqBodyJson, err := json.Marshal(reqBody)
	if err != nil {
		log.Printf("Failed to marshal prompt %v to JSON. %v", reqBody, err)
		return PromptComfyUIResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		myEnv["COMFYUI_URL"]+"/prompt",
		bytes.NewReader(reqBodyJson),
	)
	if err != nil {
		log.Printf("Failed to create prompt ComfyUI POST request: %v\n", err)
		return PromptComfyUIResponse{}, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		log.Printf("Could not connect to ComfyUI. %v\n", err)
		return PromptComfyUIResponse{}, err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("API returned bad status code. %d. Response Body: %v.\n", resp.StatusCode, resp.Body)
		return PromptComfyUIResponse{}, fmt.Errorf("API returned bad status code. %d\n", resp.StatusCode)
	}

	var promptResponse PromptComfyUIResponse
	err = json.NewDecoder(resp.Body).Decode(&promptResponse)
	if err != nil {
		log.Printf("Unable to parse prompt response returned by ComfyUI. %v\n", err)
		return PromptComfyUIResponse{}, err
	}

	return promptResponse, nil
}

func getHistoryFromComfyUI(promptID string) *HistoryEntry {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, myEnv["COMFYUI_URL"]+"/history/"+promptID, nil)
	if err != nil {
		log.Printf("Failed to create history GET request: %v\n", err)
		return nil
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		log.Printf("Could not connect to ComfyUI. %v\n", err)
		return nil
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("API returned bad status code. %d\n", resp.StatusCode)
		return nil
	}

	var historyResponse HistoryResponse
	err = json.NewDecoder(resp.Body).Decode(&historyResponse)
	if err != nil {
		log.Printf("Unable to parse history response returned by ComfyUI. %v\n", err)
		return nil
	}

	historyEntry, exists := historyResponse[promptID]
	if !exists {
		log.Printf("Unable to find promptID %v in history response returned by ComfyUI.\n", promptID)
		return nil
	}
	return &historyEntry
}

func (queueObj QueueObject) toPromptIDList() (promptIDs []string) {
	for _, val := range queueObj {
		promptID, exists := val[1].(string)
		if !exists || promptID == "" {
			log.Println("Expected promptID to be in index 1 of queue object.")
			continue
		}
		promptIDs = append(promptIDs, promptID)
	}

	return promptIDs
}

func getQueuesFromComfyUI() (queueRunning []string, queuePending []string, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, myEnv["COMFYUI_URL"]+"/queue", nil)
	if err != nil {
		log.Printf("Failed to create queue GET request: %v\n", err)
		return nil, nil, false
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		log.Printf("Could not connect to ComfyUI. %v\n", err)
		return nil, nil, false
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("API returned bad status code. %d\n", resp.StatusCode)
		return nil, nil, false
	}

	var queueResponse QueueResponse
	err = json.NewDecoder(resp.Body).Decode(&queueResponse)
	if err != nil {
		log.Printf("Unable to parse queue response returned by ComfyUI. %v\n", err)
		return nil, nil, false
	}

	queueRunning = queueResponse.QueueRunning.toPromptIDList()
	queuePending = queueResponse.QueuePending.toPromptIDList()

	return queueRunning, queuePending, true
}

func getPromptStatusFromComfyUI(promptID string) PromptStatus {
	historyEntry := getHistoryFromComfyUI(promptID)
	if historyEntry != nil {
		if historyEntry.Status.Completed {
			return StatusCompleted
		} else {
			return StatusUnknown
		}
	}

	queueRunning, queuePending, ok := getQueuesFromComfyUI()
	if !ok {
		log.Println("Unable to get queues from ComfyUI.")
		return StatusUnknown
	}

	if slices.Contains(queueRunning, promptID) {
		return StatusRunning
	} else if slices.Contains(queuePending, promptID) {
		return StatusPending
	} else {
		return StatusUnknown
	}
}

func (worker *Worker) getJobStatus(w http.ResponseWriter, req *http.Request) {
	j2pMap := &worker.jobToPromptMap
	w.Header().Set("Content-Type", "application/json")

	jobID := req.PathValue("id")

	if jobID == "" {
		log.Println("Unable to process GetJobStatusRequest with empty job_id.")
		http.Error(w, "Cannot process GetJobStatusRequest with empty job_id.", http.StatusBadRequest)
		return
	}

	promptID, exists := j2pMap.Get(jobID)
	if !exists {
		log.Printf("Cannot find existing job with job_id %v.\n", jobID)
		http.Error(w, fmt.Sprintf("Cannot find existing job with job_id %v.", jobID), http.StatusNotFound)
		return
	}

	promptStatus := getPromptStatusFromComfyUI(promptID)

	resp := GetJobStatusResponse{
		Status: promptStatus.String(),
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("Failed encoding GetJobStatusResponse %v. %v\n", resp, err)
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

	requiredEnvKeys := []string{"WORKER_ID", "WORKER_PORT", "WORKER_URL", "CONTROL_PLANE_URL", "COMFYUI_URL"}
	for _, key := range requiredEnvKeys {
		if val, ok := myEnv[key]; !ok || val == "" {
			log.Fatalf("Missing required env value for %v.", key)
		}
	}
}

func setLogFile() *os.File {
	logFile, err := os.OpenFile(
		"worker.log",
		os.O_CREATE|os.O_APPEND|os.O_WRONLY,
		0644,
	)

	if err != nil {
		log.Fatalf("failed to open worker.log: %v", err)
	}

	log.SetOutput(io.MultiWriter(os.Stderr, logFile))

	return logFile
}

func main() {
	logFile := setLogFile()
	defer logFile.Close()

	readAndValidateEnvValues()

	worker := Worker{
		jobToPromptMap: SafeJobToPromptMap{
			j2pMap: make(map[string]string),
		},
	}

	go registerToControlPlaneHeartbeat()

	http.HandleFunc("GET /health", health)
	http.HandleFunc("GET /capabilities", capabilities)
	http.HandleFunc("GET /jobs/{id}", worker.getJobStatus)
	http.HandleFunc("POST /jobs", worker.receiveJob)

	err := http.ListenAndServe(":"+myEnv["WORKER_PORT"], nil)
	if err != nil {
		log.Fatalf("Failed to start server on port %v. %v", myEnv["WORKER_PORT"], err)
	}
}
