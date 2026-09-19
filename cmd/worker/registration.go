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

func bytesToGiB(bytes int) float64 {
	gib := float64(bytes) / bytesPerGiB
	return math.Round(gib*100) / 100
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
