package seveneleven

import (
	"os"
	"testing"

	"github.com/eencloud/goeen/log"
	"github.com/google/uuid"
)

func TestProcessor_UpdateState(t *testing.T) {
	customFormat := "{{eenTimeStamp .Now}}[{{.Level}}]: {{.Message}}"
	customContext := log.NewContext(os.Stderr, customFormat, log.LevelError)
	logger := customContext.GetLogger("test", log.LevelError)
	processor := NewProcessor(logger)
	state := &TransactionState{ESN: "10037402"}

	payload := map[string]interface{}{
		"metaData": map[string]interface{}{
			"storeNumber":          "38551",
			"terminalNumber":       "01",
			"transactionSeqNumber": "1234",
		},
	}

	processor.UpdateState(payload, "192.168.1.1", state)

	if state.UUID == uuid.Nil {
		t.Error("UUID should be set")
	}
	if state.Seq != 1 {
		t.Errorf("Seq should be 1, got %d", state.Seq)
	}
}

func TestProcessor_CreateANNTStructure(t *testing.T) {
	customFormat := "{{eenTimeStamp .Now}}[{{.Level}}]: {{.Message}}"
	customContext := log.NewContext(os.Stderr, customFormat, log.LevelError)
	logger := customContext.GetLogger("test", log.LevelError)
	processor := NewProcessor(logger)

	payload := map[string]interface{}{
		"CMD": "EndTransaction",
	}

	state := &TransactionState{
		UUID: uuid.New(),
		Seq:  5,
		ESN:  "10037402",
	}

	result, err := processor.CreateANNTStructure(payload, state, 91)
	if err != nil {
		t.Errorf("CreateANNTStructure failed: %v", err)
	}

	if result["ns"] != 91 {
		t.Error("EndTransaction should have ns=91")
	}
	if result["seq"] != 0 {
		t.Error("NS91 should have seq=0")
	}
}
