.PHONY: worker-windows deploy-worker stop-worker

DESKTOP_WINDOWS_HOST := arceu@shigures-pc
DESKTOP_WINDOWS_DIR := C:\Projects\comfy-fleet-worker
WORKER_TASK := ComfyFleetWorker

worker-windows:
	mkdir -p bin
	GOOS=windows GOARCH=amd64 go build -o bin/worker.exe ./cmd/worker

deploy-worker: worker-windows
	ssh $(DESKTOP_WINDOWS_HOST) 'powershell -NoProfile -Command "Stop-ScheduledTask -TaskName $(WORKER_TASK) -ErrorAction SilentlyContinue"'
	scp bin/worker.exe $(DESKTOP_WINDOWS_HOST):worker.exe.new
	ssh $(DESKTOP_WINDOWS_HOST) 'powershell -NoProfile -Command "Move-Item -Force $$HOME\worker.exe.new $(DESKTOP_WINDOWS_DIR)\worker.exe"'
	ssh $(DESKTOP_WINDOWS_HOST) 'powershell -NoProfile -Command "Start-ScheduledTask -TaskName $(WORKER_TASK)"'

stop-worker:
	ssh $(DESKTOP_WINDOWS_HOST) 'powershell -NoProfile -Command "Stop-ScheduledTask -TaskName $(WORKER_TASK) -ErrorAction SilentlyContinue; Stop-Process -Name worker -Force -ErrorAction SilentlyContinue"'

worker-logs:
	ssh $(DESKTOP_WINDOWS_HOST) 'powershell -NoProfile -Command "Get-Content $(DESKTOP_WINDOWS_DIR)\worker.log -Wait"'