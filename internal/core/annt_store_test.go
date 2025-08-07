package core

import (
	"os"
	"testing"
	"time"

	"github.com/eencloud/goeen/log"
)

func TestANNTStore_StoreAndRetrieve(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "annt_store_test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Errorf("Failed to clean up temp dir: %v", err)
		}
	}()

	customFormat := "{{eenTimeStamp .Now}}[{{.Level}}]: {{.Message}}"
	customContext := log.NewContext(os.Stderr, customFormat, log.LevelError)
	logger := customContext.GetLogger("test", log.LevelError)

	store, err := NewANNTStore(tmpDir, 1, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("Failed to close store: %v", err)
		}
	}()

	item := ANNTItem{
		ID:             "test-001",
		RegisterIP:     "192.168.1.1",
		TransactionSeq: "1234",
		ANNTData: map[string]interface{}{
			"_pos": map[string]interface{}{
				"domain": "711pos2",
			},
		},
		Namespace: 91,
		Priority:  1,
		CreatedAt: time.Now(),
		ESN:       "10037402",
	}

	err = store.StoreANNT(item)
	if err != nil {
		t.Errorf("StoreANNT failed: %v", err)
	}

	items, err := store.GetANNTStructuresForCamera("10037402", 10)
	if err != nil {
		t.Errorf("GetANNTStructuresForCamera failed: %v", err)
	}

	if len(items) == 0 {
		t.Error("Expected at least one item")
	}
}
