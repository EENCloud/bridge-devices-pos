package seveneleven

import (
	"encoding/json"
	"testing"
	"time"
)

// Clean, focused tests for the current state machine behavior
// These tests match the actual working logic, not old expectations

func TestCurrentStateMachineBehavior(t *testing.T) {
	tests := []struct {
		name        string
		events      []map[string]interface{}
		wantNS91    int
		wantNS92    int
		description string
	}{
		{
			name: "Happy Path Complete Transaction",
			events: []map[string]interface{}{
				{"CMD": "StartTransaction"},
				{"metaData": map[string]interface{}{"storeNumber": "38551", "terminalNumber": "01", "transactionSeqNumber": "3539", "transactionType": "Sale", "operator": "OP41"}},
				{"transactionHeader": map[string]interface{}{"headerLines": []string{"7 ELEVEN", "TEST STORE"}}},
				{"cartChangeTrail": map[string]interface{}{"item": "coffee", "price": 2.99}},
				{"transactionSummary": []map[string]interface{}{{"details": "$2.99", "description": "TOTAL"}}},
				{"transactionFooter": map[string]interface{}{"footer": []string{"THANK YOU"}}},
				{"CMD": "EndTransaction"},
			},
			wantNS91:    1, // Final aggregate
			wantNS92:    2, // Start triad + cartChangeTrail (summary/footer are happy end trio)
			description: "Complete transaction with happy triad sends individual NS92s + final NS91",
		},
		{
			name: "Unknown Events With Demarcator",
			events: []map[string]interface{}{
				{"randomEvent": map[string]interface{}{"type": "chaos"}},
				{"systemError": map[string]interface{}{"code": 404}},
				{"CMD": "EndTransaction"}, // Demarcator flushes unknowns
			},
			wantNS91:    1, // Unknown events flushed as NS91
			wantNS92:    0, // No individual NS92s for unknowns or demarcators
			description: "Unknown events accumulate and flush as NS91 with demarcator",
		},
		{
			name: "Unknown Events Without Demarcator",
			events: []map[string]interface{}{
				{"randomEvent": map[string]interface{}{"type": "chaos"}},
				{"systemError": map[string]interface{}{"code": 404}},
			},
			wantNS91:    0, // No demarcator, so no flush
			wantNS92:    0, // No individual NS92s for unknowns
			description: "Unknown events accumulate but don't flush without demarcator",
		},
		{
			name: "Abandoned Transaction",
			events: []map[string]interface{}{
				{"CMD": "StartTransaction"},
				{"metaData": map[string]interface{}{"storeNumber": "38551", "terminalNumber": "01", "transactionSeqNumber": "3539", "transactionType": "Sale", "operator": "OP41"}},
				{"transactionHeader": map[string]interface{}{"headerLines": []string{"7 ELEVEN", "TEST STORE"}}},
				{"cartChangeTrail": map[string]interface{}{"item": "coffee", "price": 2.99}},
				{"CMD": "StartTransaction"}, // New start abandons previous
			},
			wantNS91:    1, // Abandoned transaction as NS91
			wantNS92:    2, // First start triad + second start triad
			description: "New StartTransaction abandons previous incomplete transaction",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			ns91Chan := make(chan ETagResult, 100)
			ns92Chan := make(chan ETagResult, 100)
			sm := NewIPBasedStateMachine(ns91Chan, ns92Chan, nil)

			// Process events
			for _, event := range tt.events {
				err := sm.ProcessEvent("192.168.1.6", mustMarshal(event))
				if err != nil {
					t.Fatalf("ProcessEvent failed: %v", err)
				}
			}

			// Give time for async processing
			time.Sleep(10 * time.Millisecond)

			// Check results
			close(ns91Chan)
			close(ns92Chan)

			ns91Count := len(ns91Chan)
			ns92Count := len(ns92Chan)

			if ns91Count != tt.wantNS91 {
				t.Errorf("NS91 count: got %d, want %d", ns91Count, tt.wantNS91)
			}
			if ns92Count != tt.wantNS92 {
				t.Errorf("NS92 count: got %d, want %d", ns92Count, tt.wantNS92)
			}

			t.Logf("✓ %s: NS91=%d, NS92=%d", tt.description, ns91Count, ns92Count)
		})
	}
}

func TestDemarcatorBehavior(t *testing.T) {
	t.Run("Demarcators Don't Send Individual NS92s", func(t *testing.T) {
		ns91Chan := make(chan ETagResult, 100)
		ns92Chan := make(chan ETagResult, 100)
		sm := NewIPBasedStateMachine(ns91Chan, ns92Chan, nil)

		// Send just a demarcator
		err := sm.ProcessEvent("192.168.1.6", mustMarshal(map[string]interface{}{
			"CMD": "EndTransaction",
		}))
		if err != nil {
			t.Fatalf("ProcessEvent failed: %v", err)
		}

		time.Sleep(10 * time.Millisecond)

		close(ns91Chan)
		close(ns92Chan)

		ns92Count := len(ns92Chan)
		if ns92Count != 0 {
			t.Errorf("Demarcators should not send NS92s, got %d", ns92Count)
		}

		t.Log("✓ Demarcators correctly don't send individual NS92s")
	})
}

func TestUnknownEventStatus(t *testing.T) {
	t.Run("Unknown Events Get UNKNOWN Status in NS91", func(t *testing.T) {
		ns91Chan := make(chan ETagResult, 100)
		ns92Chan := make(chan ETagResult, 100)
		sm := NewIPBasedStateMachine(ns91Chan, ns92Chan, nil)

		// Send unknown events + demarcator to flush
		events := []map[string]interface{}{
			{"randomEvent": map[string]interface{}{"type": "chaos"}},
			{"CMD": "EndTransaction"}, // Flush
		}

		for _, event := range events {
			err := sm.ProcessEvent("192.168.1.6", mustMarshal(event))
			if err != nil {
				t.Fatalf("ProcessEvent failed: %v", err)
			}
		}

		time.Sleep(10 * time.Millisecond)

		close(ns91Chan)
		close(ns92Chan)

		if len(ns91Chan) != 1 {
			t.Fatalf("Expected 1 NS91, got %d", len(ns91Chan))
		}

		ns91 := <-ns91Chan
		if ns91.Status == nil {
			t.Fatal("NS91 should have status for unknown events")
		}
		if *ns91.Status != StatusUnknown {
			t.Errorf("Expected UNKNOWN status, got %v", *ns91.Status)
		}

		t.Log("✓ Unknown events correctly get UNKNOWN status in NS91")
	})
}

func mustMarshal(v interface{}) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
