.PHONY: worker-windows deploy-worker stop-worker

DESKTOP_WINDOWS_HOST := arceu@shigures-pc
DESKTOP_WINDOWS_DIR := C:\Projects\comfy-fleet-worker
WORKER_TASK := ComfyFleetWorker

VRAM ?= 7
WORKFLOW ?= prompts/animagine.json
POS ?= 1girl, shameimaru aya (newsboy), touhou, glaring, clenched teeth, shaded face, cowboy shot, looking at viewer, masterpiece, high score, great score, absurdres
NEG ?= lowres, bad anatomy, bad hands, text, error, missing finger, extra digits, fewer digits, cropped, worst quality, low quality, low score, bad score, average score, signature, watermark, username, blurry, necklace
SEED ?= $(shell od -An -N4 -tu4 /dev/urandom | tr -d ' ')

PROMPT_ID ?=
OUTPUT ?= output.png
COMFYUI_URL ?= http://shigures-pc:8188

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

schedule-animagine:
	jq \
	  --arg pos "$(POS)" \
	  --arg neg "$(NEG)" \
	  --argjson vram "$(VRAM)" \
	  --argjson seed "$(SEED)" \
	  '.["3"].inputs.seed = $$seed | .["6"].inputs.text = $$pos | .["7"].inputs.text = $$neg | {required_vram_gib: $$vram, prompt: .}' \
	  $(WORKFLOW) | \
	curl -sS -X POST http://localhost:8080/schedule \
	  -H "Content-Type: application/json" \
	  --data-binary @- | jq

get-image:
	@test -n "$(PROMPT_ID)" || (echo "Usage: make get-image PROMPT_ID=<prompt-id>"; exit 1)
	@INFO="$$(curl -s "$(COMFYUI_URL)/history/$(PROMPT_ID)")"; \
	FILENAME="$$(echo "$$INFO" | jq -r '.[].outputs["9"].images[0].filename')"; \
	SUBFOLDER="$$(echo "$$INFO" | jq -r '.[].outputs["9"].images[0].subfolder')"; \
	TYPE="$$(echo "$$INFO" | jq -r '.[].outputs["9"].images[0].type')"; \
	curl -sS -o "$(OUTPUT)" "$(COMFYUI_URL)/view?filename=$$FILENAME&subfolder=$$SUBFOLDER&type=$$TYPE"; \
	echo "Saved $(OUTPUT)"