package main

import (
	"encoding/json"
	"log"
	"math"
	"net/http"
)

const bytesPerGiB = (1 << 30)

type HealthResponse struct {
	Status string `json:"status"`
}

type CapabilitiesResponse struct {
	WorkerID         string   `json:"worker_id"`
	GPU              *string  `json:"gpu"`
	VRAMGiB          *float64 `json:"vram_gib"`
	VRAMFreeGiB      *float64 `json:"vram_free_gib"`
	ComfyUIAvailable bool     `json:"comfyui_available"`
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

func handleJSONError(w http.ResponseWriter, err error) {
	log.Printf("Error while handling JSON: %s\n", err)
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}

func getComfyUISystemStats() *ComfyUISystemStats {
	resp, err := http.Get("http://localhost:8188/system_stats")
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
	capabilitiesResponse := systemStats.toCapabilities("desktop-5070ti")

	err := json.NewEncoder(w).Encode(capabilitiesResponse)
	if err != nil {
		handleJSONError(w, err)
		return
	}
}

func main() {
	http.HandleFunc("/health", health)
	http.HandleFunc("/capabilities", capabilities)

	err := http.ListenAndServe(":9000", nil)
	if err != nil {
		log.Fatalf("Failed to start server on port 9000. %v", err)
	}
}
