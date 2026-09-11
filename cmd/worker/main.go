package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"math"
	"net/http"
	"time"
)

const bytesPerGiB = (1 << 30)
const testWorkerID = "desktop-5070ti"
const controlPlaneIP = "http://10.0.0.57:8080"
const localhostIP = "http://localhost:8188"

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
}

type HealthResponse struct {
	Status string `json:"status"`
}

type WorkerCapabilities struct {
	WorkerID         string   `json:"worker_id"`
	GPU              *string  `json:"gpu"`
	VRAMGiB          *float64 `json:"vram_gib"`
	VRAMFreeGiB      *float64 `json:"vram_free_gib"`
	ComfyUIAvailable bool     `json:"comfyui_available"`
}

type CapabilitiesResponse = WorkerCapabilities
type RegisterWorkerRequest = WorkerCapabilities

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

func handleJSONError(w http.ResponseWriter, err error) {
	log.Printf("Error while handling JSON: %s\n", err)
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}

func registerToControlPlane(workerID string) {
	cap := getComfyUISystemStats().toCapabilities(workerID)
	req := RegisterWorkerRequest{
		WorkerID:         cap.WorkerID,
		GPU:              cap.GPU,
		VRAMGiB:          cap.VRAMGiB,
		VRAMFreeGiB:      cap.VRAMFreeGiB,
		ComfyUIAvailable: cap.ComfyUIAvailable,
	}

	reqJson, err := json.Marshal(req)
	if err != nil {
		log.Printf("Failed to marshal register worker request to JSON. %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, controlPlaneIP+"/workers/register", bytes.NewReader(reqJson))
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
		log.Printf("Worker %v updated successfully.\n", workerID)
	case http.StatusCreated:
		log.Printf("Worker %v registered successfully.\n", workerID)
	default:
		log.Printf("Worker %v registration failed. Status: %v. %v", workerID, resp.StatusCode, body)
	}
}

func registerToControlPlaneHeartbeat(workerID string) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	registerToControlPlane(workerID)
	log.Println("Initial register worker call.")

	for {
		t := <-ticker.C
		log.Printf("Register worker heartbeat tick at %v.\n", t)
		registerToControlPlane(workerID)
	}
}

func getComfyUISystemStats() *ComfyUISystemStats {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, localhostIP+"/system_stats", nil)
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

func (systemStats *ComfyUISystemStats) toCapabilities(workerID string) CapabilitiesResponse {
	resp := CapabilitiesResponse{
		WorkerID:         workerID,
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
		handleJSONError(w, err)
		return
	}
}

func capabilities(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	systemStats := getComfyUISystemStats()
	capabilitiesResponse := systemStats.toCapabilities(testWorkerID)

	err := json.NewEncoder(w).Encode(capabilitiesResponse)
	if err != nil {
		handleJSONError(w, err)
		return
	}
}

func main() {
	go registerToControlPlaneHeartbeat(testWorkerID)

	http.HandleFunc("/health", health)
	http.HandleFunc("/capabilities", capabilities)

	err := http.ListenAndServe(":9000", nil)
	if err != nil {
		log.Fatalf("Failed to start server on port 9000. %v", err)
	}
}
