package seveneleven

import (
	"bridge-devices-pos/internal/core"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/eencloud/goeen/log"
	"github.com/google/uuid"
)

func TestANNTArchitecture(t *testing.T) {
	logger := log.NewContext(nil, "", log.LevelError).GetLogger("annt-test", log.LevelError)

	t.Run("ANNT Structure Creation", func(t *testing.T) {
		logger := log.NewContext(os.Stderr, "", log.LevelInfo).GetLogger("test", log.LevelInfo)
		processor := NewProcessor(logger)

		payload := map[string]interface{}{
			"CMD": "EndTransaction",
			"metaData": map[string]interface{}{
				"timeStamp": "2025-01-15T10:30:00",
			},
		}

		state := &TransactionState{
			UUID: uuid.New(),
			Seq:  1,
			ESN:  "1008e25c",
		}

		anntStruct, err := processor.CreateANNTStructure(payload, state, 91)
		if err != nil {
			t.Fatalf("Failed to create ANNT structure: %v", err)
		}

		// Validate new JSON structure (no ANNT wrapper)
		if anntStruct["esn"] != "1008e25c" {
			t.Errorf("Expected esn=1008e25c, got %v", anntStruct["esn"])
		}

		if anntStruct["ns"] != 91 {
			t.Errorf("Expected ns=91, got %v", anntStruct["ns"])
		}

		if anntStruct["seq"] != 0 {
			t.Errorf("Expected seq=0 for NS91, got %v", anntStruct["seq"])
		}

		if anntStruct["uuid"] == nil {
			t.Error("Expected uuid field")
		}

		if anntStruct["mpack"] == nil {
			t.Error("Expected mpack field")
		}

		mpack, ok := anntStruct["mpack"].(map[string]interface{})
		if !ok {
			t.Error("Expected mpack to be a map")
		} else if mpack["_pos"] == nil {
			t.Error("Expected _pos field inside mpack")
		}
	})

	t.Run("ANNT Storage and Retrieval", func(t *testing.T) {
		anntStore, err := core.NewANNTStore("data/test_annt_unittest.db", 1, logger)
		if err != nil {
			t.Fatalf("Failed to create ANNT store: %v", err)
		}
		defer func() {
			if err := anntStore.Close(); err != nil {
				t.Errorf("Failed to close ANNT store: %v", err)
			}
		}()

		// Create test ANNT - simplified structure for Lua
		testANNT := map[string]interface{}{
			"uuid": "test-uuid-123",
			"seq":  1,
			"esn":  "1008e25c",
			"ns":   92,
			"mpack": map[string]interface{}{
				"_pos": map[string]interface{}{
					"domain": "711pos2",
				},
			},
		}

		anntItem := core.ANNTItem{
			ID:             "test-unit-1",
			RegisterIP:     "10.0.140.24",
			TransactionSeq: "1234",
			ANNTData:       testANNT,
			Namespace:      92,
			Priority:       1,
			CreatedAt:      time.Now(),
			ESN:            "1008e25c",
		}

		// Store ANNT
		err = anntStore.StoreANNT(anntItem)
		if err != nil {
			t.Fatalf("Failed to store ANNT: %v", err)
		}

		// Retrieve ANNTs
		retrievedANNTs, err := anntStore.GetANNTStructuresForCamera("1008e25c", 100)
		if err != nil {
			t.Fatalf("Failed to retrieve ANNTs: %v", err)
		}

		if len(retrievedANNTs) != 1 {
			t.Errorf("Expected 1 ANNT, got %d", len(retrievedANNTs))
		}

		// Test "fetched = delivered" behavior
		secondRetrieval, err := anntStore.GetANNTStructuresForCamera("1008e25c", 100)
		if err != nil {
			t.Fatalf("Failed second retrieval: %v", err)
		}

		if len(secondRetrieval) != 0 {
			t.Errorf("Expected 0 ANNTs on second retrieval, got %d", len(secondRetrieval))
		}
	})

	t.Run("ProcessorAdapter Interface", func(t *testing.T) {
		logger := log.NewContext(os.Stderr, "", log.LevelInfo).GetLogger("test", log.LevelInfo)

		adapter := NewProcessorAdapter(logger)

		// Test with EndTransaction
		payload := map[string]interface{}{
			"CMD": "EndTransaction",
			"metaData": map[string]interface{}{
				"timeStamp": "2025-01-15T10:30:00",
			},
		}

		state := &TransactionState{
			UUID: uuid.New(),
			Seq:  1,
			ESN:  "1008e25c",
		}

		anntJSON, err := adapter.CreateANNTStructure(payload, state, 91)
		if err != nil {
			t.Fatalf("CreateANNTStructure failed: %v", err)
		}

		// Parse the returned JSON
		var anntStruct map[string]interface{}
		if err := json.Unmarshal(anntJSON, &anntStruct); err != nil {
			t.Fatalf("Failed to parse returned JSON: %v", err)
		}

		// Check the new JSON structure directly
		if anntStruct["esn"] != "1008e25c" {
			t.Errorf("Expected esn=1008e25c, got %v", anntStruct["esn"])
		}

		if anntStruct["ns"] != float64(91) {
			t.Errorf("Expected ns=91, got %v", anntStruct["ns"])
		}
	})
}
