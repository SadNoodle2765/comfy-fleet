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
	"time"

	"github.com/joho/godotenv"
)

var myEnv map[string]string

const bytesPerGiB = (1 << 30)

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
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

func receiveJob(w http.ResponseWriter, req *http.Request) {
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

	go registerToControlPlaneHeartbeat()

	http.HandleFunc("GET /health", health)
	http.HandleFunc("GET /capabilities", capabilities)
	http.HandleFunc("POST /jobs", receiveJob)

	err := http.ListenAndServe(":"+myEnv["WORKER_PORT"], nil)
	if err != nil {
		log.Fatalf("Failed to start server on port %v. %v", myEnv["WORKER_PORT"], err)
	}
}
