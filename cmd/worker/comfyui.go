package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"syscall"
	"time"
)

func getComfyUISystemStats() *ComfyUISystemStats {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, myEnv["COMFYUI_URL"]+"/system_stats", nil)
	if err != nil {
		log.Printf("Failed to create system_stats GET request: %v\n", err)
		return nil
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		log.Printf("Could not connect to ComfyUI. %v\n", err)
		return nil
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("API returned bad status code. %d\n", resp.StatusCode)
		return nil
	}

	var systemStats ComfyUISystemStats
	err = json.NewDecoder(resp.Body).Decode(&systemStats)
	if err != nil {
		log.Printf("Unable to parse system stats returned by ComfyUI. %v\n", err)
		return nil
	}

	return &systemStats
}

func sendPromptToComfyUI(prompt map[string]any) (PromptComfyUIResponse, FailureType, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	reqBody := PromptComfyUIRequest{
		Prompt: prompt,
	}

	reqBodyJson, err := json.Marshal(reqBody)
	if err != nil {
		log.Printf("Failed to marshal prompt %v to JSON. %v", reqBody, err)
		return PromptComfyUIResponse{}, NonRetryableFailure, err
	}

	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		myEnv["COMFYUI_URL"]+"/prompt",
		bytes.NewReader(reqBodyJson),
	)
	if err != nil {
		log.Printf("Failed to create prompt ComfyUI POST request: %v\n", err)
		return PromptComfyUIResponse{}, NonRetryableFailure, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		log.Printf("Could not connect to ComfyUI. %v\n", err)
		if errors.Is(err, syscall.ECONNREFUSED) {
			return PromptComfyUIResponse{}, RetryableFailure, err
		}
		return PromptComfyUIResponse{}, AmbiguousFailure, err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			log.Printf("failed to read ComfyUI error response: %v", readErr)
		}
		err := fmt.Errorf(
			"ComfyUI returned status %d. Response Body: %s.",
			resp.StatusCode,
			string(body),
		)
		switch resp.StatusCode {
		case http.StatusBadRequest:
			return PromptComfyUIResponse{}, NonRetryableFailure, err
		case http.StatusInternalServerError:
			return PromptComfyUIResponse{}, AmbiguousFailure, err
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return PromptComfyUIResponse{}, RetryableFailure, err
		default:
			return PromptComfyUIResponse{}, AmbiguousFailure, err
		}
	}

	var promptResponse PromptComfyUIResponse
	err = json.NewDecoder(resp.Body).Decode(&promptResponse)
	if err != nil {
		log.Printf("Unable to parse prompt response returned by ComfyUI. %v\n", err)
		return PromptComfyUIResponse{}, AmbiguousFailure, err
	}

	if promptResponse.PromptID == "" {
		return PromptComfyUIResponse{}, AmbiguousFailure, errors.New("ComfyUI returned empty promptID.")
	}

	return promptResponse, NoFailure, nil
}

func getHistoryFromComfyUI(promptID string) *HistoryEntry {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, myEnv["COMFYUI_URL"]+"/history/"+promptID, nil)
	if err != nil {
		log.Printf("Failed to create history GET request: %v\n", err)
		return nil
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		log.Printf("Could not connect to ComfyUI. %v\n", err)
		return nil
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("API returned bad status code. %d\n", resp.StatusCode)
		return nil
	}

	var historyResponse HistoryResponse
	err = json.NewDecoder(resp.Body).Decode(&historyResponse)
	if err != nil {
		log.Printf("Unable to parse history response returned by ComfyUI. %v\n", err)
		return nil
	}

	historyEntry, exists := historyResponse[promptID]
	if !exists {
		log.Printf("Unable to find promptID %v in history response returned by ComfyUI.\n", promptID)
		return nil
	}
	return &historyEntry
}

func getQueuesFromComfyUI() (queueRunning []string, queuePending []string, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, myEnv["COMFYUI_URL"]+"/queue", nil)
	if err != nil {
		log.Printf("Failed to create queue GET request: %v\n", err)
		return nil, nil, false
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		log.Printf("Could not connect to ComfyUI. %v\n", err)
		return nil, nil, false
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("API returned bad status code. %d\n", resp.StatusCode)
		return nil, nil, false
	}

	var queueResponse QueueResponse
	err = json.NewDecoder(resp.Body).Decode(&queueResponse)
	if err != nil {
		log.Printf("Unable to parse queue response returned by ComfyUI. %v\n", err)
		return nil, nil, false
	}

	queueRunning = queueResponse.QueueRunning.toPromptIDList()
	queuePending = queueResponse.QueuePending.toPromptIDList()

	return queueRunning, queuePending, true
}

func getPromptStatusFromComfyUI(promptID string) PromptStatus {
	historyEntry := getHistoryFromComfyUI(promptID)
	if historyEntry != nil {
		if historyEntry.Status.Completed {
			return StatusCompleted
		} else {
			return StatusUnknown
		}
	}

	queueRunning, queuePending, ok := getQueuesFromComfyUI()
	if !ok {
		log.Println("Unable to get queues from ComfyUI.")
		return StatusUnknown
	}

	if slices.Contains(queueRunning, promptID) {
		return StatusRunning
	} else if slices.Contains(queuePending, promptID) {
		return StatusPending
	} else {
		return StatusUnknown
	}
}

func getImageMetadataFromComfyUI(promptID string) (image ComfyImage, err error) {
	history := getHistoryFromComfyUI(promptID)
	if history == nil {
		return image, fmt.Errorf("could not get history from ComfyUI with prompt_id %v.", promptID)
	}

	for _, node := range history.Outputs {
		if len(node.Images) > 0 {
			return node.Images[0], nil
		}
	}

	return image, fmt.Errorf("could not get image from history response from ComfyUI for prompt_id %v. %v", promptID, history.Outputs)
}

func getImageFromComfyUI(ctx context.Context, promptID string) (resp *http.Response, err error) {
	imageMetadata, err := getImageMetadataFromComfyUI(promptID)
	if err != nil {
		return resp, fmt.Errorf("failed getting image metadata for promptID %v. %w", promptID, err)
	}

	u, err := url.Parse(myEnv["COMFYUI_URL"] + "/view")
	if err != nil {
		return resp, fmt.Errorf("failed parsing URL. %w", err)
	}

	q := u.Query()
	q.Add("filename", imageMetadata.Filename)
	q.Add("subfolder", imageMetadata.Subfolder)
	q.Add("type", imageMetadata.Type)
	u.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return resp, fmt.Errorf("failed to create view GET request: %w", err)
	}

	resp, err = httpClient.Do(httpReq)
	if err != nil {
		return resp, fmt.Errorf("could not connect to ComfyUI. %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return resp, fmt.Errorf("ComfyUI returned bad status code. %d", resp.StatusCode)
	}

	return resp, nil
}

func (queueObj QueueObject) toPromptIDList() (promptIDs []string) {
	for _, val := range queueObj {
		promptID, exists := val[1].(string)
		if !exists || promptID == "" {
			log.Println("Expected promptID to be in index 1 of queue object.")
			continue
		}
		promptIDs = append(promptIDs, promptID)
	}

	return promptIDs
}
