package main

import "sync"

type SafeJobRecordMap struct {
	mu           sync.RWMutex
	jobRecordMap map[string]JobRecord
}

func (jMap *SafeJobRecordMap) Get(jobID string) (JobRecord, bool) {
	jMap.mu.RLock()
	defer jMap.mu.RUnlock()

	jobRecord, exists := jMap.jobRecordMap[jobID]
	if !exists {
		return JobRecord{}, false
	}
	return jobRecord, true
}

func (jMap *SafeJobRecordMap) Put(jobRecord JobRecord) (recordExists bool) {
	jMap.mu.Lock()
	defer jMap.mu.Unlock()

	_, recordExists = jMap.jobRecordMap[jobRecord.JobID]
	jMap.jobRecordMap[jobRecord.JobID] = jobRecord

	return recordExists
}
