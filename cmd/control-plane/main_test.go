package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func stringPtr(s string) *string {
	return &s
}

func floatPtr(v float64) *float64 {
	return &v
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestEligibleWorkers(t *testing.T) {
	laptopWorker := Worker{
		WorkerID:         "laptop",
		GPU:              stringPtr("RTX 4070"),
		VRAMGiB:          floatPtr(8),
		VRAMFreeGiB:      floatPtr(4),
		ComfyUIAvailable: true,
		LastSeen:         time.Now(),
	}

	desktopWorker := Worker{
		WorkerID:         "desktop",
		GPU:              stringPtr("RTX 5070 Ti"),
		VRAMGiB:          floatPtr(16),
		VRAMFreeGiB:      floatPtr(14),
		ComfyUIAvailable: true,
		LastSeen:         time.Now(),
	}

	desktopWorkerComfyUIUnavailable := desktopWorker
	desktopWorkerComfyUIUnavailable.ComfyUIAvailable = false

	desktopWorkerNilVRAMFreeGiB := desktopWorker
	desktopWorkerNilVRAMFreeGiB.VRAMFreeGiB = nil

	desktopWorkerOffline := desktopWorker
	desktopWorkerOffline.LastSeen = time.Now().Add(-1 * time.Hour)

	tests := []struct {
		name          string
		workers       []Worker
		requiredVram  float64
		wantWorkerIDs []string
	}{
		{
			name:          "chooses all qualifying workers",
			workers:       []Worker{desktopWorker, laptopWorker},
			requiredVram:  1,
			wantWorkerIDs: []string{desktopWorker.WorkerID, laptopWorker.WorkerID},
		},
		{
			name:          "chooses only qualifying workers",
			workers:       []Worker{desktopWorker, laptopWorker},
			requiredVram:  12,
			wantWorkerIDs: []string{desktopWorker.WorkerID},
		},
		{
			name:          "qualifies worker with exactly required free VRAM",
			workers:       []Worker{desktopWorker, laptopWorker},
			requiredVram:  14,
			wantWorkerIDs: []string{desktopWorker.WorkerID},
		},
		{
			name:          "returns no workers when none qualify",
			workers:       []Worker{desktopWorker, laptopWorker},
			requiredVram:  20,
			wantWorkerIDs: []string{},
		},
		{
			name:          "ignores worker when ComfyUI is unavailable",
			workers:       []Worker{desktopWorkerComfyUIUnavailable, laptopWorker},
			requiredVram:  1,
			wantWorkerIDs: []string{laptopWorker.WorkerID},
		},
		{
			name:          "ignores worker with unknown free VRAM",
			workers:       []Worker{desktopWorkerNilVRAMFreeGiB, laptopWorker},
			requiredVram:  1,
			wantWorkerIDs: []string{laptopWorker.WorkerID},
		},
		{
			name:          "ignores offline worker",
			workers:       []Worker{desktopWorkerOffline, laptopWorker},
			requiredVram:  1,
			wantWorkerIDs: []string{laptopWorker.WorkerID},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {

			controlPlane := ControlPlane{
				workerMap: SafeWorkerMap{
					workerMap: make(map[string]Worker),
				},
				jobRecordMap: SafeJobRecordMap{
					jobRecordMap: make(map[string]JobRecord),
				},
			}

			for _, w := range test.workers {
				controlPlane.workerMap.Put(w)
			}

			workers := controlPlane.eligibleWorkers(test.requiredVram)
			var gotWorkerIDs []string

			for _, worker := range workers {
				gotWorkerIDs = append(gotWorkerIDs, worker.WorkerID)
			}

			if !slices.Equal(test.wantWorkerIDs, gotWorkerIDs) {
				t.Errorf(
					"got worker IDs %v, want %v",
					gotWorkerIDs,
					test.wantWorkerIDs,
				)
			}
		})
	}
}

func createScheduleWorkerTestWorkerServer(t *testing.T, status string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/jobs" {
				t.Errorf("unexpected path %s", r.URL.Path)
			}

			if r.Method != http.MethodPost {
				t.Errorf("unexpected method %s", r.Method)
			}

			var job Job
			err := json.NewDecoder(r.Body).Decode(&job)
			if err != nil {
				t.Errorf("failed to decode Job. %v", err)
			}

			resp := SendJobResponse{
				JobID:    job.ID,
				PromptID: "fake-prompt",
				Status:   status,
			}

			w.Header().Set("Content-Type", "application/json")
			err = json.NewEncoder(w).Encode(resp)
			if err != nil {
				t.Errorf("failed to encode SendJobResponse %v to JSON.", resp)
			}
		}))
}

func TestScheduleWorker(t *testing.T) {
	testClient := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Host {
			case "bad-desktop", "bad-desktop-2":
				return nil, syscall.ECONNREFUSED
			default:
				return http.DefaultTransport.RoundTrip(r)
			}
		}),
		Timeout: 10 * time.Second,
	}
	goodWorkerServer := createScheduleWorkerTestWorkerServer(t, "accepted")
	defer goodWorkerServer.Close()

	desktopWorker := Worker{
		WorkerID:         "desktop",
		URL:              goodWorkerServer.URL,
		GPU:              stringPtr("RTX 5070 Ti"),
		VRAMGiB:          floatPtr(16),
		VRAMFreeGiB:      floatPtr(14),
		ComfyUIAvailable: true,
		LastSeen:         time.Now(),
	}

	badDesktopWorker := desktopWorker
	badDesktopWorker.WorkerID = "bad-desktop"
	badDesktopWorker.VRAMFreeGiB = floatPtr(16)
	badDesktopWorker.URL = "http://bad-desktop"

	badDesktopWorker2 := badDesktopWorker
	badDesktopWorker2.WorkerID = "bad-desktop-2"
	badDesktopWorker2.URL = "http://bad-desktop-2"

	testWorkerMap := map[string]Worker{
		desktopWorker.WorkerID: desktopWorker,
	}

	workerMapWithBadWorker := map[string]Worker{
		desktopWorker.WorkerID:    desktopWorker,
		badDesktopWorker.WorkerID: badDesktopWorker,
	}

	workerMapWithAllBadWorkers := map[string]Worker{
		badDesktopWorker.WorkerID:  badDesktopWorker,
		badDesktopWorker2.WorkerID: badDesktopWorker2,
	}

	tests := []struct {
		name           string
		reqBody        string
		workerMap      map[string]Worker
		wantStatusCode int
		wantWorkerID   string
	}{
		{
			name:           "returns 200 for happy path",
			reqBody:        `{"required_vram_gib":4, "prompt": {"bleh": "bleh"}}`,
			workerMap:      testWorkerMap,
			wantStatusCode: http.StatusOK,
			wantWorkerID:   desktopWorker.WorkerID,
		},
		{
			name:           "fails over when first worker connection is refused",
			reqBody:        `{"required_vram_gib":4, "prompt": {"bleh": "bleh"}}`,
			workerMap:      workerMapWithBadWorker,
			wantStatusCode: http.StatusOK,
			wantWorkerID:   desktopWorker.WorkerID,
		},
		{
			name:           "returns 503 when all eligible worker connections are refused",
			reqBody:        `{"required_vram_gib":4, "prompt": {"bleh": "bleh"}}`,
			workerMap:      workerMapWithAllBadWorkers,
			wantStatusCode: http.StatusServiceUnavailable,
		},
		{
			name:           "returns 503 if cannot find available worker",
			reqBody:        `{"required_vram_gib":100, "prompt": {"bleh": "bleh"}}`,
			workerMap:      testWorkerMap,
			wantStatusCode: http.StatusServiceUnavailable,
		},
		{
			name:           "returns 400 for requiredVRAM value = 0",
			reqBody:        `{"required_vram_gib":0, "prompt": {"bleh": "bleh"}}`,
			workerMap:      testWorkerMap,
			wantStatusCode: http.StatusBadRequest,
		},
		{
			name:           "returns 400 for requiredVRAM value < 0",
			reqBody:        `{"required_vram_gib":-5, "prompt": {"bleh": "bleh"}}`,
			workerMap:      testWorkerMap,
			wantStatusCode: http.StatusBadRequest,
		},
		{
			name:           "returns 400 for malformed JSON",
			reqBody:        `{not json}`,
			workerMap:      testWorkerMap,
			wantStatusCode: http.StatusBadRequest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controlPlane := ControlPlane{
				workerMap: SafeWorkerMap{
					workerMap: test.workerMap,
				},
				jobRecordMap: SafeJobRecordMap{
					jobRecordMap: make(map[string]JobRecord),
				},
				httpClient: testClient,
			}

			recorder := httptest.NewRecorder()

			req := httptest.NewRequest(
				http.MethodPost,
				"/schedule",
				strings.NewReader(test.reqBody),
			)

			controlPlane.ScheduleWorker(recorder, req)
			if recorder.Code != test.wantStatusCode {
				t.Fatalf("got status code %v, expected %v.", recorder.Code, test.wantStatusCode)
			}
			if recorder.Code == http.StatusOK {
				var response ScheduleResponse
				err := json.NewDecoder(recorder.Body).Decode(&response)
				if err != nil {
					t.Fatalf("failed to decode response: %v", err)
				}

				if response.Worker.WorkerID != test.wantWorkerID {
					t.Fatalf("got workerID %v, expected %v.", response.Worker.WorkerID, test.wantWorkerID)
				}
			}
		})
	}

	t.Run("jobID should stay the same across worker SendJob requests", func(t *testing.T) {
		var callCount atomic.Int32
		var firstJobID string
		var secondJobID string
		fakeClient := &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				count := callCount.Add(1)

				switch count {
				case 1:
					// first worker, definite failure
					var job Job
					if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
						t.Errorf("failed to decode job: %v", err)
						return nil, err
					}
					firstJobID = job.ID
					return nil, syscall.ECONNREFUSED
				case 2:
					var job Job
					if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
						t.Errorf("failed to decode job: %v", err)
					}

					secondJobID = job.ID
					body, err := json.Marshal(SendJobResponse{
						JobID:    job.ID,
						PromptID: "fake-prompt-id",
						Status:   "accepted",
					})
					if err != nil {
						return nil, err
					}

					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(bytes.NewReader(body)),
					}, nil
				}
				return nil, fmt.Errorf("unexpected HTTP call %d.", count)
			}),
		}

		rejectWorker := desktopWorker
		rejectWorker.WorkerID = "reject-worker"
		rejectWorker.VRAMFreeGiB = floatPtr(16)

		acceptWorker := desktopWorker
		acceptWorker.WorkerID = "accept-worker"
		acceptWorker.VRAMFreeGiB = floatPtr(14)

		controlPlane := ControlPlane{
			workerMap: SafeWorkerMap{
				workerMap: map[string]Worker{
					rejectWorker.WorkerID: rejectWorker,
					acceptWorker.WorkerID: acceptWorker,
				},
			},
			jobRecordMap: SafeJobRecordMap{
				jobRecordMap: make(map[string]JobRecord),
			},
			httpClient: fakeClient,
		}

		recorder := httptest.NewRecorder()

		req := httptest.NewRequest(
			http.MethodPost,
			"/schedule",
			strings.NewReader(`{"required_vram_gib":4, "prompt": {"bleh": "bleh"}}`),
		)

		controlPlane.ScheduleWorker(recorder, req)

		if recorder.Code != http.StatusOK {
			t.Fatalf("got status %v, expected 200", recorder.Code)
		}

		if firstJobID != secondJobID {
			t.Fatalf("job IDs differ: first=%v second=%v", firstJobID, secondJobID)
		}

		var response ScheduleResponse
		if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
			t.Fatal(err)
		}

		if response.JobID != firstJobID {
			t.Fatalf("response job ID %v differs from dispatched ID %v", response.JobID, firstJobID)
		}

		if response.Worker.WorkerID != acceptWorker.WorkerID {
			t.Fatalf(
				"got worker %v, expected %v",
				response.Worker.WorkerID,
				acceptWorker.WorkerID,
			)
		}
	})
}

func TestDispatchJob(t *testing.T) {
	t.Run("first worker retryable failure, second worker succeeds same job.", func(t *testing.T) {
		expectedPromptID := "fake-prompt-id"
		var worker2JobID string
		worker2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("unexpected method %s", r.Method)
				return
			}

			if r.URL.Path != "/jobs" {
				t.Errorf("unexpected path %s", r.URL.Path)
				return
			}
			var job Job
			if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
				t.Errorf("failed to decode worker2 job: %v", err)
				return
			}

			worker2JobID = job.ID
			resp := SendJobResponse{
				JobID:    job.ID,
				PromptID: expectedPromptID,
				Status:   "accepted",
			}

			w.Header().Set("Content-Type", "application/json")
			err := json.NewEncoder(w).Encode(resp)
			if err != nil {
				t.Errorf("failed to encode SendJobResponse %v to JSON.", resp)
			}
		}))
		defer worker2Server.Close()

		testJob := Job{
			ID: "test-job-id",
			Prompt: map[string]any{
				"bleh": "bleh",
			},
		}

		worker1 := Worker{
			WorkerID:         "worker1",
			URL:              "http://127.0.0.1:65534",
			GPU:              new("RTX 5070 Ti"),
			VRAMGiB:          floatPtr(16),
			VRAMFreeGiB:      floatPtr(14),
			ComfyUIAvailable: true,
			LastSeen:         time.Now(),
		}
		worker2 := worker1
		worker2.WorkerID = "worker2"
		worker2.URL = worker2Server.URL

		eligibleWorkers := []Worker{worker1, worker2}

		cp := ControlPlane{
			httpClient: &http.Client{
				Timeout: 10 * time.Second,
			},
		}

		chosenWorker, promptID, err := cp.dispatchJob(testJob, eligibleWorkers)

		if err != nil {
			t.Fatalf("got error calling dispatchJob %v.", err)
		}

		if worker2JobID != testJob.ID {
			t.Fatalf("expected job_id = %v. got %v.", testJob.ID, worker2JobID)
		}

		if chosenWorker.WorkerID != worker2.WorkerID {
			t.Fatalf("expected chosen worker to be %v. got %v.", worker2.WorkerID, chosenWorker.WorkerID)
		}

		if promptID != expectedPromptID {
			t.Fatalf("expected returned promptID to be %v. got %v.", expectedPromptID, promptID)
		}
	})

	t.Run("first worker ambiguous failure, get to see if actually sent and found job.", func(t *testing.T) {
		expectedPromptID := "fake-prompt-id"
		var postJobID string
		var getJobID string
		var callCounter atomic.Int32
		workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count := callCounter.Add(1)

			switch r.Method {
			case http.MethodPost:
				var job Job
				if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
					t.Errorf("failed to decode worker job: %v", err)
					return
				}

				if count != 1 {
					t.Errorf("expected only the first call to be POST.")
					return
				}

				postJobID = job.ID

				// Throw ambiguous error so next call is GET to see if job was actually recevied by worker
				http.Error(w, "Ambiguous Error", http.StatusInternalServerError)
				return
			case http.MethodGet:
				if count != 2 {
					t.Errorf("expected only the second call to be GET.")
					return
				}

				getJobID = strings.TrimPrefix(r.URL.Path, "/jobs/")

				resp := GetJobResponse{
					JobID:    postJobID,
					PromptID: expectedPromptID,
					Status:   "accepted",
				}
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(resp)
				if err != nil {
					t.Errorf("failed to encode SendJobResponse %v to JSON.", resp)
				}
			}
		}))
		defer workerServer.Close()

		worker2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("should not have been called.")
		}))
		defer worker2Server.Close()

		testJob := Job{
			ID: "test-job-id",
			Prompt: map[string]any{
				"bleh": "bleh",
			},
		}

		worker1 := Worker{
			WorkerID:         "worker1",
			URL:              workerServer.URL,
			GPU:              new("RTX 5070 Ti"),
			VRAMGiB:          floatPtr(16),
			VRAMFreeGiB:      floatPtr(14),
			ComfyUIAvailable: true,
			LastSeen:         time.Now(),
		}
		worker2 := worker1
		worker2.WorkerID = "worker2"
		worker2.URL = worker2Server.URL

		eligibleWorkers := []Worker{worker1, worker2}

		cp := ControlPlane{
			httpClient: &http.Client{
				Timeout: 10 * time.Second,
			},
		}

		chosenWorker, promptID, err := cp.dispatchJob(testJob, eligibleWorkers)

		if err != nil {
			t.Fatalf("got error calling dispatchJob %v.", err)
		}

		if getJobID != postJobID {
			t.Fatalf("expected getJobID = postJobID. got %v, %v.", getJobID, postJobID)
		}

		if chosenWorker.WorkerID != worker1.WorkerID {
			t.Fatalf("expected chosen worker to be %v. got %v.", worker1.WorkerID, chosenWorker.WorkerID)
		}

		if promptID != expectedPromptID {
			t.Fatalf("expected returned promptID to be %v. got %v.", expectedPromptID, promptID)
		}

		if callCounter.Load() != 2 {
			t.Fatalf("expected worker 1 server to be called exactly twice. got %d.", callCounter.Load())
		}
	})

	t.Run("first worker ambiguous failure, get and see job not found, so retry on same worker and succeed", func(t *testing.T) {
		expectedPromptID := "fake-prompt-id"
		post1JobID := ""
		post2JobID := ""
		getJobID := ""
		var callCounter atomic.Int32
		workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count := callCounter.Add(1)

			switch r.Method {
			case http.MethodPost:
				var job Job
				if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
					t.Errorf("failed to decode worker job: %v", err)
					return
				}

				if count == 1 {
					post1JobID = job.ID
					// Throw ambigious error so next call is GET to see if job was actually recevied by worker
					http.Error(w, "Ambiguous Error", http.StatusInternalServerError)
					return
				} else if count == 3 {
					post2JobID = job.ID
					resp := SendJobResponse{
						JobID:    post2JobID,
						PromptID: expectedPromptID,
						Status:   "accepted",
					}
					w.Header().Set("Content-Type", "application/json")
					err := json.NewEncoder(w).Encode(resp)
					if err != nil {
						t.Errorf("failed to encode SendJobResponse %v to JSON.", resp)
						return
					}
				} else {
					t.Errorf("only expected POST to be called on count 1 and 3. got %d.", count)
					return
				}
			case http.MethodGet:
				if count != 2 {
					t.Errorf("expected only the second call to be GET.")
					return
				}

				getJobID = strings.TrimPrefix(r.URL.Path, "/jobs/")

				// Throw 404 error so next call is POST to retry sending job to same worker
				http.Error(w, "Not Found Error", http.StatusNotFound)
				return
			}
		}))
		defer workerServer.Close()

		worker2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("should not have been called.")
		}))

		defer worker2Server.Close()

		testJob := Job{
			ID: "test-job-id",
			Prompt: map[string]any{
				"bleh": "bleh",
			},
		}

		worker1 := Worker{
			WorkerID:         "worker1",
			URL:              workerServer.URL,
			GPU:              new("RTX 5070 Ti"),
			VRAMGiB:          floatPtr(16),
			VRAMFreeGiB:      floatPtr(14),
			ComfyUIAvailable: true,
			LastSeen:         time.Now(),
		}
		worker2 := worker1
		worker2.WorkerID = "worker2"
		worker2.URL = worker2Server.URL

		eligibleWorkers := []Worker{worker1, worker2}

		cp := ControlPlane{
			httpClient: &http.Client{
				Timeout: 10 * time.Second,
			},
		}

		chosenWorker, promptID, err := cp.dispatchJob(testJob, eligibleWorkers)

		if err != nil {
			t.Fatalf("got error calling dispatchJob %v.", err)
		}

		if getJobID != testJob.ID || post1JobID != testJob.ID || post2JobID != testJob.ID {
			t.Fatalf("expected getJobID and postJobIDs to all be %v. got %v, %v, %v.", testJob.ID, getJobID, post1JobID, post2JobID)
		}

		if chosenWorker.WorkerID != worker1.WorkerID {
			t.Fatalf("expected chosen worker to be %v. got %v.", worker1.WorkerID, chosenWorker.WorkerID)
		}

		if promptID != expectedPromptID {
			t.Fatalf("expected returned promptID to be %v. got %v.", expectedPromptID, promptID)
		}

		if callCounter.Load() != 3 {
			t.Fatalf("expected worker 1 server to be called exactly three times. got %d.", callCounter.Load())
		}
	})

	t.Run("first worker ambiguous failure, get and see job not found, retry is retryable error doesn't go to next worker.", func(t *testing.T) {
		postJobID := ""
		connectionRefusedJobID := ""
		getJobID := ""
		var callCounter atomic.Int32
		var worker2CallCounter atomic.Int32
		fakeClient := &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				count := callCounter.Add(1)
				switch count {
				case 3:
					var job Job
					if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
						t.Errorf("failed to decode worker job: %v", err)
						return nil, err
					}
					connectionRefusedJobID = job.ID
					return nil, syscall.ECONNREFUSED
				default:
					return http.DefaultTransport.RoundTrip(r)
				}
			}),
		}
		workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count := callCounter.Load()
			switch r.Method {
			case http.MethodPost:
				var job Job
				if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
					t.Errorf("failed to decode worker job: %v", err)
					return
				}

				if count == 1 {
					postJobID = job.ID
					// Throw ambigious error so next call is GET to see if job was actually recevied by worker
					http.Error(w, "Ambiguous Error", http.StatusInternalServerError)
					return
				} else {
					t.Errorf("only expected POST to be called on count 1 and 3. got %d.", count)
					return
				}
			case http.MethodGet:
				if count != 2 {
					t.Errorf("expected only the second call to be GET.")
					return
				}

				getJobID = strings.TrimPrefix(r.URL.Path, "/jobs/")

				// Throw 404 error so next call is POST to retry sending job to same worker
				http.Error(w, "Not Found Error", http.StatusNotFound)
				return
			}
		}))
		defer workerServer.Close()

		worker2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			worker2CallCounter.Add(1)
		}))

		defer worker2Server.Close()

		testJob := Job{
			ID: "test-job-id",
			Prompt: map[string]any{
				"bleh": "bleh",
			},
		}

		worker1 := Worker{
			WorkerID:         "worker1",
			URL:              workerServer.URL,
			GPU:              new("RTX 5070 Ti"),
			VRAMGiB:          floatPtr(16),
			VRAMFreeGiB:      floatPtr(14),
			ComfyUIAvailable: true,
			LastSeen:         time.Now(),
		}
		worker2 := worker1
		worker2.WorkerID = "worker2"
		worker2.URL = worker2Server.URL

		eligibleWorkers := []Worker{worker1, worker2}

		cp := ControlPlane{
			httpClient: fakeClient,
		}

		chosenWorker, promptID, err := cp.dispatchJob(testJob, eligibleWorkers)

		if err == nil {
			t.Fatalf("expected error calling dispatchJob %v.", err)
		}

		if chosenWorker.WorkerID != "" {
			t.Fatalf("expected there to be no chosenWorker.")
		}

		if promptID != "" {
			t.Fatalf("expected there to be no promptID.")
		}

		if getJobID != testJob.ID || postJobID != testJob.ID || connectionRefusedJobID != testJob.ID {
			t.Fatalf("expected getJobID and postJobIDs to all be %v. got %v, %v, %v.", testJob.ID, getJobID, postJobID, connectionRefusedJobID)
		}

		if callCounter.Load() != 3 {
			t.Fatalf("expected exactly 3 HTTP attempts, got %d.", callCounter.Load())
		}

		if worker2CallCounter.Load() != 0 {
			t.Fatalf("expected worker 2 to not be called at all.")
		}
	})
}
