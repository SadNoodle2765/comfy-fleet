package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func stringPtr(s string) *string {
	return &s
}

func floatPtr(v float64) *float64 {
	return &v
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
			workerMap := SafeWorkerMap{
				workerMap: make(map[string]Worker),
			}

			for _, w := range test.workers {
				workerMap.workerMap[w.WorkerID] = w
			}

			workers := workerMap.eligibleWorkers(test.requiredVram)
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

func TestScheduleWorker(t *testing.T) {
	goodWorkerServer := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/jobs" {
				t.Fatalf("unexpected path %s", r.URL.Path)
			}

			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method %s", r.Method)
			}

			w.WriteHeader(http.StatusOK)
		}))
	defer goodWorkerServer.Close()

	badWorkerServer := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))

	defer badWorkerServer.Close()

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
	badDesktopWorker.URL = badWorkerServer.URL

	badDesktopWorker2 := badDesktopWorker
	badDesktopWorker2.WorkerID = "bad-desktop-2"

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
			reqBody:        `{"required_vram_gib":4}`,
			workerMap:      testWorkerMap,
			wantStatusCode: http.StatusOK,
			wantWorkerID:   desktopWorker.WorkerID,
		},
		{
			name:           "ignores server that refuses job POST",
			reqBody:        `{"required_vram_gib":4}`,
			workerMap:      workerMapWithBadWorker,
			wantStatusCode: http.StatusOK,
			wantWorkerID:   desktopWorker.WorkerID,
		},
		{
			name:           "returns 503 if all eligible worker refuses job POST",
			reqBody:        `{"required_vram_gib":4}`,
			workerMap:      workerMapWithAllBadWorkers,
			wantStatusCode: http.StatusServiceUnavailable,
		},
		{
			name:           "returns 503 if cannot find available worker",
			reqBody:        `{"required_vram_gib":100}`,
			workerMap:      testWorkerMap,
			wantStatusCode: http.StatusServiceUnavailable,
		},
		{
			name:           "returns 400 for requiredVRAM value = 0",
			reqBody:        `{"required_vram_gib":0}`,
			workerMap:      testWorkerMap,
			wantStatusCode: http.StatusBadRequest,
		},
		{
			name:           "returns 400 for requiredVRAM value < 0",
			reqBody:        `{"required_vram_gib":-5}`,
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
			testSafeWorkerMap := SafeWorkerMap{
				workerMap: test.workerMap,
			}
			recorder := httptest.NewRecorder()

			req := httptest.NewRequest(
				http.MethodPost,
				"/schedule",
				strings.NewReader(test.reqBody),
			)

			testSafeWorkerMap.ScheduleWorker(recorder, req)
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
}
