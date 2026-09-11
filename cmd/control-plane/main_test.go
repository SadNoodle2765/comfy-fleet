package main

import (
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
