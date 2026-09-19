package main

import (
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
)

var myEnv map[string]string

const bytesPerGiB = (1 << 30)

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
}

func readAndValidateEnvValues() {
	var err error
	myEnv, err = godotenv.Read()
	if err != nil {
		log.Fatalf("No .env file found. %v", err)
	}

	requiredEnvKeys := []string{"WORKER_ID", "WORKER_PORT", "WORKER_URL", "CONTROL_PLANE_URL", "COMFYUI_URL"}
	for _, key := range requiredEnvKeys {
		if val, ok := myEnv[key]; !ok || val == "" {
			log.Fatalf("Missing required env value for %v.", key)
		}
	}
}

func setLogFile() *os.File {
	logFile, err := os.OpenFile(
		"worker.log",
		os.O_CREATE|os.O_APPEND|os.O_WRONLY,
		0644,
	)

	if err != nil {
		log.Fatalf("failed to open worker.log: %v", err)
	}

	log.SetOutput(io.MultiWriter(os.Stderr, logFile))

	return logFile
}

func main() {
	logFile := setLogFile()
	defer logFile.Close()

	readAndValidateEnvValues()

	worker := Worker{
		jobMap: SafeJobMap{
			jobs: make(map[string]*JobState),
		},
	}

	go registerToControlPlaneHeartbeat()

	http.HandleFunc("GET /health", health)
	http.HandleFunc("GET /capabilities", capabilities)
	http.HandleFunc("GET /jobs/{id}", worker.getJob)
	http.HandleFunc("GET /jobs/{id}/image", worker.getImage)
	http.HandleFunc("POST /jobs", worker.receiveJob)

	err := http.ListenAndServe(":"+myEnv["WORKER_PORT"], nil)
	if err != nil {
		log.Fatalf("Failed to start server on port %v. %v", myEnv["WORKER_PORT"], err)
	}
}
