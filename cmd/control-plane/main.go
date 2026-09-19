package main

import (
	"log"
	"net/http"
	"time"

	"github.com/joho/godotenv"
)

var myEnv map[string]string

const offlineTime = 30 * time.Second

type ControlPlane struct {
	workerMap    SafeWorkerMap
	jobRecordMap SafeJobRecordMap
	httpClient   HTTPDoer
}

func readAndValidateEnvValues() {
	var err error
	myEnv, err = godotenv.Read()
	if err != nil {
		log.Fatalf("No .env file found. %v", err)
	}

	requiredEnvKeys := []string{"CONTROL_PLANE_PORT"}
	for _, key := range requiredEnvKeys {
		if val, ok := myEnv[key]; !ok || val == "" {
			log.Fatalf("Missing required env value for %v.", key)
		}
	}
}

func main() {
	readAndValidateEnvValues()

	controlPlane := ControlPlane{
		workerMap: SafeWorkerMap{
			workerMap: make(map[string]Worker),
		},
		jobRecordMap: SafeJobRecordMap{
			jobRecordMap: make(map[string]JobRecord),
		},
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}

	http.HandleFunc("GET /workers", controlPlane.ListWorkers)
	http.HandleFunc("GET /workers/{id}", controlPlane.GetWorker)
	http.HandleFunc("GET /jobs/{id}", controlPlane.GetJobRecord)
	http.HandleFunc("GET /jobs/{id}/image", controlPlane.GetJobImage)
	http.HandleFunc("POST /workers/register", controlPlane.RegisterWorker)
	http.HandleFunc("POST /schedule", controlPlane.ScheduleWorker)

	err := http.ListenAndServe(":"+myEnv["CONTROL_PLANE_PORT"], nil)
	if err != nil {
		log.Fatalf("Failed to start server on port %v. %v", myEnv["CONTROL_PLANE_PORT"], err)
	}
}
