package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func stringPtr(s string) *string {
	return &s
}

func floatPtr(v float64) *float64 {
	return &v
}

func TestHealth(t *testing.T) {
	t.Run("health check should return 200 and status 'ok'", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(
			http.MethodGet,
			"/health",
			nil,
		)

		health(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("expected code %v, got %v.", http.StatusOK, recorder.Code)
		}

		var healthResp HealthResponse
		if err := json.NewDecoder(recorder.Body).Decode(&healthResp); err != nil {
			t.Fatal(err)
		}

		if healthResp.Status != "ok" {
			t.Fatalf("expected status 'ok', got '%v'", healthResp.Status)
		}
	})
}

func TestReceiveJob(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		wantStatusCode int
		wantJobID      string
		wantStatus     string
	}{
		{
			name:           "happy path should have status 200",
			body:           `{"job_id": "good-job-id"}`,
			wantStatusCode: http.StatusOK,
			wantJobID:      "good-job-id",
			wantStatus:     "accepted",
		},
		{
			name:           "empty jobID should have status 400",
			body:           `{"job_id": ""}`,
			wantStatusCode: http.StatusBadRequest,
		},
		{
			name:           "missing jobID should have status 400",
			body:           `{"vram": 200}`,
			wantStatusCode: http.StatusBadRequest,
		},
		{
			name:           "malformed json should have status 400",
			body:           `{"job_i`,
			wantStatusCode: http.StatusBadRequest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPost,
				"/jobs",
				strings.NewReader(test.body),
			)

			receiveJob(recorder, request)

			if recorder.Code != test.wantStatusCode {
				t.Fatalf("expected status code %v. got %v.\n", test.wantStatusCode, recorder.Code)
			}

			if recorder.Code != http.StatusOK {
				return
			}

			var receiveJobResp ReceiveJobResponse
			if err := json.NewDecoder(recorder.Body).Decode(&receiveJobResp); err != nil {
				t.Fatal(err)
			}

			if test.wantJobID != receiveJobResp.JobID {
				t.Fatalf("expected jobID %v. got %v.\n", test.wantJobID, receiveJobResp.JobID)
			}

			if test.wantStatus != receiveJobResp.Status {
				t.Fatalf("expected status %v. got %v.\n", test.wantStatus, receiveJobResp.Status)
			}
		})
	}
}

func TestCapabilities(t *testing.T) {
	testDevice := Device{
		Name:      "test-device",
		Type:      "test-type",
		Index:     0,
		VRAMTotal: 16 * bytesPerGiB,
		VRAMFree:  14 * bytesPerGiB,
	}
	comfyUIServer := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path != "/system_stats" {
				t.Fatalf("unexpected path %s", req.URL.Path)
			}

			if req.Method != http.MethodGet {
				t.Fatalf("unexpected method %s", req.Method)
			}

			systemStats := ComfyUISystemStats{
				Devices: []Device{testDevice},
			}

			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(systemStats); err != nil {
				t.Fatalf("failed to encode systemStats %v to JSON.", systemStats)
			}
		},
	))

	defer comfyUIServer.Close()

	badComfyUIServer := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, req *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		},
	))

	defer badComfyUIServer.Close()

	t.Run("capabilities happy path should have GPU + VRAM populated", func(t *testing.T) {
		oldEnv := myEnv
		myEnv = map[string]string{
			"WORKER_ID":   "test-worker",
			"WORKER_URL":  "http://test-worker",
			"COMFYUI_URL": comfyUIServer.URL,
		}
		defer func() {
			myEnv = oldEnv
		}()

		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/capabilities", nil)

		capabilities(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("expected code %v, got %v.", http.StatusOK, recorder.Code)
		}

		var workerCapabilities WorkerCapabilities
		if err := json.NewDecoder(recorder.Body).Decode(&workerCapabilities); err != nil {
			t.Fatal(err)
		}

		if !workerCapabilities.ComfyUIAvailable {
			t.Fatal("Expected ComfyUIAvailable = true. Returned false.")
		}

		if workerCapabilities.GPU == nil {
			t.Fatal("expected GPU to be populated")
		}

		if workerCapabilities.VRAMGiB == nil {
			t.Fatal("expected VRAMGiB to be populated")
		}

		if workerCapabilities.VRAMFreeGiB == nil {
			t.Fatal("expected VRAMFreeGiB to be populated")
		}

		if *workerCapabilities.GPU != testDevice.Name ||
			*workerCapabilities.VRAMGiB != bytesToGiB(testDevice.VRAMTotal) ||
			*workerCapabilities.VRAMFreeGiB != bytesToGiB(testDevice.VRAMFree) {
			t.Fatalf("mismatch in worker capabilities and test device. worker cap: %v, device: %v.\n", workerCapabilities, testDevice)
		}

		if workerCapabilities.WorkerID != "test-worker" {
			t.Fatalf("expected WorkerID to be 'test-worker'. got %v.\n", workerCapabilities.WorkerID)
		}

		if workerCapabilities.URL != "http://test-worker" {
			t.Fatalf("expected URL to be 'http://test-worker'. got %v.\n", workerCapabilities.URL)
		}
	})

	t.Run("capabilities should have ComfyUIAvailable = false and nil GPU + VRAM if ComfyUI system_stats call fails.", func(t *testing.T) {
		oldEnv := myEnv
		myEnv = map[string]string{
			"WORKER_ID":   "test-worker",
			"WORKER_URL":  "http://test-worker",
			"COMFYUI_URL": badComfyUIServer.URL,
		}
		defer func() {
			myEnv = oldEnv
		}()

		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/capabilities", nil)

		capabilities(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("expected code %v, got %v.", http.StatusOK, recorder.Code)
		}

		var workerCapabilities WorkerCapabilities
		if err := json.NewDecoder(recorder.Body).Decode(&workerCapabilities); err != nil {
			t.Fatal(err)
		}

		if workerCapabilities.ComfyUIAvailable {
			t.Fatal("Expected ComfyUIAvailable = false. Returned true.")
		}

		if workerCapabilities.GPU != nil {
			t.Fatal("expected GPU to be nil")
		}

		if workerCapabilities.VRAMGiB != nil {
			t.Fatal("expected VRAMGiB to be nil")
		}

		if workerCapabilities.VRAMFreeGiB != nil {
			t.Fatal("expected VRAMFreeGiB to be nil")
		}

		if workerCapabilities.WorkerID != "test-worker" {
			t.Fatalf("expected WorkerID to be 'test-worker'. got %v.\n", workerCapabilities.WorkerID)
		}

		if workerCapabilities.URL != "http://test-worker" {
			t.Fatalf("expected URL to be 'http://test-worker'. got %v.\n", workerCapabilities.URL)
		}
	})
}
