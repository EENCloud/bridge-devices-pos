package api

import (
	"testing"
	"time"
)

func TestIncomingDataLog_Basic(t *testing.T) {
	log := incomingDataLog{
		Timestamp:  time.Now(),
		RemoteAddr: "127.0.0.1:8080",
		Data:       "test data",
		EventType:  "test_event",
	}

	if log.RemoteAddr != "127.0.0.1:8080" {
		t.Errorf("Expected remote addr '127.0.0.1:8080', got '%s'", log.RemoteAddr)
	}

	if log.EventType != "test_event" {
		t.Errorf("Expected event type 'test_event', got '%s'", log.EventType)
	}

	if log.Data != "test data" {
		t.Error("Data not set correctly")
	}
}
