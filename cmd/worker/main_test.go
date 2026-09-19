package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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
			body:           `{"job_id": "good-job-id", "prompt": {"bleh": "bleh"}}`,
			wantStatusCode: http.StatusOK,
			wantJobID:      "good-job-id",
			wantStatus:     "accepted",
		},
		{
			name:           "empty jobID should have status 400",
			body:           `{"job_id": "", "prompt": {"bleh": "bleh"}}`,
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

	promptComfyCount := 0

	comfyUIServer := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, req *http.Request) {
			promptComfyCount++
			if req.URL.Path != "/prompt" {
				t.Errorf("unexpected path %s", req.URL.Path)
				return
			}

			if req.Method != http.MethodPost {
				t.Errorf("unexpected method %s", req.Method)
				return
			}

			resp := PromptComfyUIResponse{
				PromptID:   fmt.Sprintf("test_prompt_id_%d", promptComfyCount),
				Number:     0,
				NodeErrors: nil,
			}

			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				t.Errorf("failed to encode promptComfyUIResponse %v to JSON.", resp)
				return
			}
		},
	))

	defer comfyUIServer.Close()

	oldEnv := myEnv
	myEnv = map[string]string{
		"COMFYUI_URL": comfyUIServer.URL,
	}
	defer func() {
		myEnv = oldEnv
	}()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			worker := Worker{
				jobMap: SafeJobMap{
					jobs: make(map[string]*JobState),
				},
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPost,
				"/jobs",
				strings.NewReader(test.body),
			)

			worker.receiveJob(recorder, request)

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

	t.Run("receiveJob with duplicate jobID should only call prompt once.", func(t *testing.T) {
		promptComfyCount = 0
		worker := Worker{
			jobMap: SafeJobMap{
				jobs: make(map[string]*JobState),
			},
		}
		testBody := `{"job_id": "good-job-id", "prompt": {"bleh": "bleh"}}`
		recorder1 := httptest.NewRecorder()
		request1 := httptest.NewRequest(
			http.MethodPost,
			"/jobs",
			strings.NewReader(testBody),
		)

		recorder2 := httptest.NewRecorder()
		request2 := httptest.NewRequest(
			http.MethodPost,
			"/jobs",
			strings.NewReader(testBody),
		)

		var receiveJobResp1 ReceiveJobResponse
		var receiveJobResp2 ReceiveJobResponse
		worker.receiveJob(recorder1, request1)
		worker.receiveJob(recorder2, request2)

		if recorder1.Code != http.StatusOK {
			t.Fatalf("expected status code %v. got %v.\n", http.StatusOK, recorder1.Code)
		}
		if recorder2.Code != http.StatusOK {
			t.Fatalf("expected status code %v. got %v.\n", http.StatusOK, recorder2.Code)
		}

		if err := json.NewDecoder(recorder1.Body).Decode(&receiveJobResp1); err != nil {
			t.Fatal(err)
		}
		if err := json.NewDecoder(recorder2.Body).Decode(&receiveJobResp2); err != nil {
			t.Fatal(err)
		}

		promptId1 := receiveJobResp1.PromptID
		promptId2 := receiveJobResp2.PromptID

		if promptId1 != promptId2 {
			t.Fatalf("expected promptId1 and promptId2 to be the same. got %v and %v.\n", promptId1, promptId2)
		}
		if promptComfyCount != 1 {
			t.Fatalf("expected comfyUI /prompt POST call to be called exactly once. got %d times.\n", promptComfyCount)
		}
	})

	t.Run("concurrent receiveJob with same job_id should only call comfyUI once and have same promptId", func(t *testing.T) {
		var promptCount atomic.Int32

		concurrentServer := httptest.NewServer(http.HandlerFunc(
			func(w http.ResponseWriter, req *http.Request) {
				count := promptCount.Add(1)
				if req.URL.Path != "/prompt" {
					t.Errorf("unexpected path %s", req.URL.Path)
					return
				}

				if req.Method != http.MethodPost {
					t.Errorf("unexpected method %s", req.Method)
					return
				}

				resp := PromptComfyUIResponse{
					PromptID:   fmt.Sprintf("test_prompt_id_%d", count),
					Number:     0,
					NodeErrors: nil,
				}

				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(resp); err != nil {
					t.Errorf("failed to encode promptComfyUIResponse %v to JSON.", resp)
					return
				}
			},
		))

		defer concurrentServer.Close()

		oldEnv := myEnv
		myEnv = map[string]string{
			"COMFYUI_URL": concurrentServer.URL,
		}
		defer func() {
			myEnv = oldEnv
		}()

		expectedPromptID := "test_prompt_id_1"

		worker := Worker{
			jobMap: SafeJobMap{
				jobs: make(map[string]*JobState),
			},
		}

		var wg sync.WaitGroup
		wg.Add(5)

		receiveJob := func() {
			defer wg.Done()

			testBody := `{"job_id": "test-job-id", "prompt": {"bleh": "bleh"}}`
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPost,
				"/jobs",
				strings.NewReader(testBody),
			)

			worker.receiveJob(recorder, request)

			var resp ReceiveJobResponse
			if recorder.Code != http.StatusOK {
				t.Errorf("expected status code %v. got %v.\n", http.StatusOK, recorder.Code)
				return
			}
			if err := json.NewDecoder(recorder.Body).Decode(&resp); err != nil {
				t.Error(err)
				return
			}

			if resp.PromptID != expectedPromptID {
				t.Errorf("expected promptID = %v. got %v.\n", expectedPromptID, resp.PromptID)
				return
			}
		}

		for range 5 {
			go receiveJob()
		}

		wg.Wait()

		if promptCount.Load() != 1 {
			t.Fatalf("expected promptComfyCount = 1. got %d.\n", promptCount.Load())
		}
	})
	t.Run("for five concurrent same job_id, if first one fails retryable error, second one succeeds case", func(t *testing.T) {
		var promptCount atomic.Int32

		concurrentServer := httptest.NewServer(http.HandlerFunc(
			func(w http.ResponseWriter, req *http.Request) {
				count := promptCount.Add(1)
				if req.URL.Path != "/prompt" {
					t.Errorf("unexpected path %s", req.URL.Path)
					return
				}

				if req.Method != http.MethodPost {
					t.Errorf("unexpected method %s", req.Method)
					return
				}

				if count == 1 {
					time.Sleep(200 * time.Millisecond)
					http.Error(w, "Retryable error.", http.StatusServiceUnavailable)
					return
				}

				resp := PromptComfyUIResponse{
					PromptID:   fmt.Sprintf("test_prompt_id_%d", count),
					Number:     0,
					NodeErrors: nil,
				}

				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(resp); err != nil {
					t.Errorf("failed to encode promptComfyUIResponse %v to JSON.", resp)
					return
				}
			},
		))

		defer concurrentServer.Close()

		oldEnv := myEnv
		myEnv = map[string]string{
			"COMFYUI_URL": concurrentServer.URL,
		}
		defer func() {
			myEnv = oldEnv
		}()

		worker := Worker{
			jobMap: SafeJobMap{
				jobs: make(map[string]*JobState),
			},
		}

		var wg sync.WaitGroup
		wg.Add(5)

		var successCount atomic.Int32
		var failureCount atomic.Int32

		expectedPromptID := "test_prompt_id_2"
		receiveJob := func() {
			defer wg.Done()

			testBody := `{"job_id": "test-job-id", "prompt": {"bleh": "bleh"}}`
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPost,
				"/jobs",
				strings.NewReader(testBody),
			)

			worker.receiveJob(recorder, request)

			switch recorder.Code {
			case http.StatusOK:
				successCount.Add(1)

				var resp ReceiveJobResponse
				if err := json.NewDecoder(recorder.Body).Decode(&resp); err != nil {
					t.Error(err)
					return
				}
				if resp.PromptID != expectedPromptID {
					t.Errorf("expected promptID = %v. got %v.\n", expectedPromptID, resp.PromptID)
					return
				}
			case http.StatusInternalServerError:
				failureCount.Add(1)
			default:
				t.Errorf("unexpected status code %d", recorder.Code)
			}
		}

		for range 5 {
			go receiveJob()
		}

		wg.Wait()

		if promptCount.Load() != 2 {
			t.Fatalf("expected promptComfyCount = 2. got %d.\n", promptCount.Load())
		}

		if got := successCount.Load(); got != 4 {
			t.Fatalf("expected 4 successful worker requests, got %d", got)
		}

		if got := failureCount.Load(); got != 1 {
			t.Fatalf("expected 1 failed worker request, got %d", got)
		}
	})

	t.Run("for five concurrent same job_id, if first one fails non-retryable error, the rest should also fail", func(t *testing.T) {
		var promptCount atomic.Int32

		failServer := httptest.NewServer(http.HandlerFunc(
			func(w http.ResponseWriter, req *http.Request) {
				promptCount.Add(1)
				if req.URL.Path != "/prompt" {
					t.Errorf("unexpected path %s", req.URL.Path)
					return
				}

				if req.Method != http.MethodPost {
					t.Errorf("unexpected method %s", req.Method)
					return
				}

				time.Sleep(200 * time.Millisecond)
				http.Error(w, "Non-retryable error.", http.StatusBadRequest)
			},
		))
		defer failServer.Close()

		oldEnv := myEnv
		myEnv = map[string]string{
			"COMFYUI_URL": failServer.URL,
		}
		defer func() {
			myEnv = oldEnv
		}()

		worker := Worker{
			jobMap: SafeJobMap{
				jobs: make(map[string]*JobState),
			},
		}

		var failureCount atomic.Int32
		var wg sync.WaitGroup
		wg.Add(5)

		receiveJob := func() {
			defer wg.Done()
			testBody := `{"job_id": "test-job-id", "prompt": {"bleh": "bleh"}}`
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPost,
				"/jobs",
				strings.NewReader(testBody),
			)

			worker.receiveJob(recorder, request)

			if recorder.Code != http.StatusInternalServerError {
				t.Errorf("expected status code %v. got %v.", http.StatusInternalServerError, recorder.Code)
			} else {
				failureCount.Add(1)
			}
		}

		for range 5 {
			go receiveJob()
		}

		wg.Wait()

		if promptCount.Load() != 1 {
			t.Fatalf("expected comfy prompt to only be called once. got %d.", promptCount.Load())
		}
		if failureCount.Load() != 5 {
			t.Fatalf("expected failure count to be 5. got %d.", failureCount.Load())
		}
	})

	t.Run("for five concurrent same job_id, if first one fails ambiguous error, the rest should also fail", func(t *testing.T) {
		var promptCount atomic.Int32

		failServer := httptest.NewServer(http.HandlerFunc(
			func(w http.ResponseWriter, req *http.Request) {
				promptCount.Add(1)
				if req.URL.Path != "/prompt" {
					t.Errorf("unexpected path %s", req.URL.Path)
					return
				}

				if req.Method != http.MethodPost {
					t.Errorf("unexpected method %s", req.Method)
					return
				}

				time.Sleep(200 * time.Millisecond)
				resp := struct {
					Bad string
				}{
					Bad: "bad",
				}

				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(resp); err != nil {
					t.Errorf("failed to encode %v to JSON.", resp)
					return
				}
			},
		))
		defer failServer.Close()

		oldEnv := myEnv
		myEnv = map[string]string{
			"COMFYUI_URL": failServer.URL,
		}
		defer func() {
			myEnv = oldEnv
		}()

		worker := Worker{
			jobMap: SafeJobMap{
				jobs: make(map[string]*JobState),
			},
		}

		var failureCount atomic.Int32
		var wg sync.WaitGroup
		wg.Add(5)

		receiveJob := func() {
			defer wg.Done()
			testBody := `{"job_id": "test-job-id", "prompt": {"bleh": "bleh"}}`
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPost,
				"/jobs",
				strings.NewReader(testBody),
			)

			worker.receiveJob(recorder, request)

			if recorder.Code != http.StatusInternalServerError {
				t.Errorf("expected status code %v. got %v.", http.StatusInternalServerError, recorder.Code)
			} else {
				failureCount.Add(1)
			}
		}

		for range 5 {
			go receiveJob()
		}

		wg.Wait()

		if promptCount.Load() != 1 {
			t.Fatalf("expected comfy prompt to only be called once. got %d.", promptCount.Load())
		}
		if failureCount.Load() != 5 {
			t.Fatalf("expected failure count to be 5. got %d.", failureCount.Load())
		}
	})
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
