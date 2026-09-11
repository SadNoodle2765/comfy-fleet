package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestChooseWorker(t *testing.T) {
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
		name         string
		workers      []Worker
		requiredVram float64
		wantWorkerID string
		wantFound    bool
	}{
		{
			name:         "chooses worker with most free VRAM when multiple qualify",
			workers:      []Worker{desktopWorker, laptopWorker},
			requiredVram: 1,
			wantWorkerID: desktopWorker.WorkerID,
			wantFound:    true,
		},
		{
			name:         "chooses only qualifying worker",
			workers:      []Worker{desktopWorker, laptopWorker},
			requiredVram: 12,
			wantWorkerID: desktopWorker.WorkerID,
			wantFound:    true,
		},
		{
			name:         "qualifies worker with exactly required free VRAM",
			workers:      []Worker{desktopWorker, laptopWorker},
			requiredVram: 14,
			wantWorkerID: desktopWorker.WorkerID,
			wantFound:    true,
		},
		{
			name:         "returns no worker when none qualify",
			workers:      []Worker{desktopWorker, laptopWorker},
			requiredVram: 20,
			wantFound:    false,
		},
		{
			name:         "ignores worker when ComfyUI is unavailable",
			workers:      []Worker{desktopWorkerComfyUIUnavailable, laptopWorker},
			requiredVram: 1,
			wantWorkerID: laptopWorker.WorkerID,
			wantFound:    true,
		},
		{
			name:         "ignores worker with unknown free VRAM",
			workers:      []Worker{desktopWorkerNilVRAMFreeGiB, laptopWorker},
			requiredVram: 1,
			wantWorkerID: laptopWorker.WorkerID,
			wantFound:    true,
		},
		{
			name:         "ignores offline worker",
			workers:      []Worker{desktopWorkerOffline, laptopWorker},
			requiredVram: 1,
			wantWorkerID: laptopWorker.WorkerID,
			wantFound:    true,
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

			gotWorker, gotFound := workerMap.chooseWorker(test.requiredVram)

			if gotFound != test.wantFound {
				t.Fatalf("gotFound = %v, wantFound = %v", gotFound, test.wantFound)
			}

			if test.wantFound && gotWorker.WorkerID != test.wantWorkerID {
				if gotWorker.VRAMFreeGiB == nil {
					t.Fatalf(
						"expected worker ID %q to have non-nil VRAMFreeGiB.",
						gotWorker.WorkerID,
					)
				}
				t.Errorf(
					"got worker ID %q with free VRAM %.2f, want %q",
					gotWorker.WorkerID,
					*gotWorker.VRAMFreeGiB,
					test.wantWorkerID,
				)
			}
		})
	}
}

func TestScheduleWorker(t *testing.T) {
	desktopWorker := Worker{
		WorkerID:         "desktop",
		GPU:              stringPtr("RTX 5070 Ti"),
		VRAMGiB:          floatPtr(16),
		VRAMFreeGiB:      floatPtr(14),
		ComfyUIAvailable: true,
		LastSeen:         time.Now(),
	}
	testWorkerMap := SafeWorkerMap{
		workerMap: map[string]Worker{
			desktopWorker.WorkerID: desktopWorker,
		},
	}
	tests := []struct {
		name           string
		reqBody        string
		wantStatusCode int
		wantWorkerID   string
	}{
		{
			name:           "returns 200 for happy path",
			reqBody:        `{"required_vram_gib":4}`,
			wantStatusCode: http.StatusOK,
			wantWorkerID:   desktopWorker.WorkerID,
		},
		{
			name:           "returns 503 if cannot find available worker",
			reqBody:        `{"required_vram_gib":100}`,
			wantStatusCode: http.StatusServiceUnavailable,
		},
		{
			name:           "returns 400 for requiredVRAM value = 0",
			reqBody:        `{"required_vram_gib":0}`,
			wantStatusCode: http.StatusBadRequest,
		},
		{
			name:           "returns 400 for requiredVRAM value < 0",
			reqBody:        `{"required_vram_gib":-5}`,
			wantStatusCode: http.StatusBadRequest,
		},
		{
			name:           "returns 400 for malformed JSON",
			reqBody:        `{not json}`,
			wantStatusCode: http.StatusBadRequest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()

			req := httptest.NewRequest(
				http.MethodPost,
				"/schedule",
				strings.NewReader(test.reqBody),
			)

			testWorkerMap.ScheduleWorker(recorder, req)
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
