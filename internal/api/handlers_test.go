package api

import (
	"testing"
	"time"
)

func TestLogIncomingData(t *testing.T) {
	// Clear any existing data
	incomingDataMutex.Lock()
	incomingData = nil
	incomingDataMutex.Unlock()

	// Test logging basic data
	testData := map[string]interface{}{
		"CMD":  "StartTransaction",
		"test": "data",
	}

	LogIncomingData("127.0.0.1:8080", testData)

	incomingDataMutex.RLock()
	defer incomingDataMutex.RUnlock()

	if len(incomingData) != 1 {
		t.Errorf("Expected 1 log entry, got %d", len(incomingData))
	}

	entry := incomingData[0]
	if entry.RemoteAddr != "127.0.0.1:8080" {
		t.Errorf("Expected remote addr '127.0.0.1:8080', got '%s'", entry.RemoteAddr)
	}

	if entry.EventType != "cmd_StartTransaction" {
		t.Errorf("Expected event type 'cmd_StartTransaction', got '%s'", entry.EventType)
	}

	dataMap, ok := entry.Data.(map[string]interface{})
	if !ok {
		t.Error("Data not stored as map")
	} else if dataMap["CMD"] != "StartTransaction" {
		t.Error("Data not stored correctly")
	}
}

func TestLogIncomingData_NoCommand(t *testing.T) {
	// Clear any existing data
	incomingDataMutex.Lock()
	incomingData = nil
	incomingDataMutex.Unlock()

	testData := map[string]interface{}{
		"field1": "value1",
		"field2": "value2",
	}

	LogIncomingData("127.0.0.1:8080", testData)

	incomingDataMutex.RLock()
	defer incomingDataMutex.RUnlock()

	if len(incomingData) != 1 {
		t.Errorf("Expected 1 log entry, got %d", len(incomingData))
	}

	entry := incomingData[0]
	if entry.EventType != "event_2_fields" {
		t.Errorf("Expected event type 'event_2_fields', got '%s'", entry.EventType)
	}
}

func TestLogIncomingData_NonMapData(t *testing.T) {
	// Clear any existing data
	incomingDataMutex.Lock()
	incomingData = nil
	incomingDataMutex.Unlock()

	testData := "simple string data"

	LogIncomingData("127.0.0.1:8080", testData)

	incomingDataMutex.RLock()
	defer incomingDataMutex.RUnlock()

	if len(incomingData) != 1 {
		t.Errorf("Expected 1 log entry, got %d", len(incomingData))
	}

	entry := incomingData[0]
	if entry.EventType != "pos_event" {
		t.Errorf("Expected event type 'pos_event', got '%s'", entry.EventType)
	}

	if entry.Data != testData {
		t.Error("String data not stored correctly")
	}
}

func TestLogIncomingData_MaxLogs(t *testing.T) {
	// Clear any existing data
	incomingDataMutex.Lock()
	incomingData = nil
	originalMax := maxIncomingLogs
	maxIncomingLogs = 3 // Set small limit for testing
	incomingDataMutex.Unlock()

	// Add more than max logs
	for i := 0; i < 5; i++ {
		LogIncomingData("127.0.0.1:8080", map[string]interface{}{"test": i})
	}

	incomingDataMutex.RLock()
	logCount := len(incomingData)
	incomingDataMutex.RUnlock()

	if logCount != 3 {
		t.Errorf("Expected max 3 logs, got %d", logCount)
	}

	// Restore original max
	incomingDataMutex.Lock()
	maxIncomingLogs = originalMax
	incomingDataMutex.Unlock()
}

func TestIncomingDataLogTimestamp(t *testing.T) {
	// Clear any existing data
	incomingDataMutex.Lock()
	incomingData = nil
	incomingDataMutex.Unlock()

	before := time.Now()
	LogIncomingData("127.0.0.1:8080", map[string]interface{}{"test": "data"})
	after := time.Now()

	incomingDataMutex.RLock()
	defer incomingDataMutex.RUnlock()

	if len(incomingData) != 1 {
		t.Error("Expected 1 log entry")
		return
	}

	timestamp := incomingData[0].Timestamp
	if timestamp.Before(before) || timestamp.After(after) {
		t.Error("Timestamp not within expected range")
	}
}
