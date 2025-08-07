package seveneleven

import (
	"encoding/json"
	"fmt"

	"github.com/eencloud/goeen/log"
	"github.com/google/uuid"
)

var posNamespaceUUID = uuid.MustParse("a37b6c48-5282-4469-8710-34863f684a13")

const (
	DefaultESN = "1000face"
	InvalidESN = "00000000"
)

// TransactionState holds the unique state for a single ongoing POS transaction.
type TransactionState struct {
	UUID uuid.UUID
	Seq  uint16
	ESN  string
}

// Processor handles the business logic of transforming register data into ANNT structures.
type Processor struct {
	logger *log.Logger
}

// NewProcessor creates a new processor.
func NewProcessor(logger *log.Logger) *Processor {
	return &Processor{
		logger: logger,
	}
}

// updateState manages the transaction lifecycle, creating a UUIDv5 on start.
func (p *Processor) updateState(payload map[string]interface{}, ip string, state *TransactionState) {
	// If we see metadata, generate the definitive UUIDv5.
	if metaData, ok := payload["metaData"].(map[string]interface{}); ok {
		store, _ := metaData["storeNumber"].(string)
		terminal, _ := metaData["terminalNumber"].(string)
		txn, _ := metaData["transactionSeqNumber"].(string)

		if store != "" && terminal != "" && txn != "" {
			nameString := fmt.Sprintf("%s:%s:%s:%s", ip, store, terminal, txn)
			state.UUID = uuid.NewSHA1(posNamespaceUUID, []byte(nameString))
			state.Seq = 0
			p.logger.Infof("Generated new UUIDv5 %s for transaction %s", state.UUID, txn)
		}
	} else if cmd, ok := payload["CMD"].(string); ok && cmd == "StartTransaction" && state.UUID == uuid.Nil {
		// If it's a start without metadata, generate a temporary random UUID.
		state.UUID = uuid.New()
		state.Seq = 0
		p.logger.Infof("Generated temporary UUIDv4 %s for new transaction", state.UUID)
	}
	state.Seq++
}

// UpdateState manages the transaction lifecycle, creating a UUIDv5 on start.
func (p *Processor) UpdateState(payload map[string]interface{}, ip string, state *TransactionState) {
	p.updateState(payload, ip, state)
}

func (p *Processor) CreateANNTStructure(payload map[string]interface{}, state *TransactionState, namespace int) (map[string]interface{}, error) {
	var posData map[string]interface{}

	if _, ok := payload["_pos"].(map[string]interface{}); ok {
		posData = payload
	} else {
		posData = map[string]interface{}{
			"_pos": payload,
		}
	}

	sequence := int(state.Seq)
	if namespace == 91 {
		sequence = 0
	}

	return map[string]interface{}{
		"uuid": state.UUID.String(),
		"seq":  sequence,
		"esn":  state.ESN,
		"ns":   namespace,
		"mpack": map[string]interface{}{
			"_pos": posData["_pos"],
		},
	}, nil
}

// ProcessorAdapter implements the statemachine.ETagProcessor interface
type ProcessorAdapter struct {
	*Processor
}

// NewProcessorAdapter creates a processor adapter for state machine integration
func NewProcessorAdapter(logger *log.Logger) *ProcessorAdapter {
	return &ProcessorAdapter{
		Processor: NewProcessor(logger),
	}
}

func (pa *ProcessorAdapter) CreateANNTStructure(payload map[string]interface{}, state *TransactionState, namespace int) ([]byte, error) {
	anntStruct, err := pa.Processor.CreateANNTStructure(payload, state, namespace)
	if err != nil {
		return nil, err
	}

	return json.Marshal(anntStruct)
}
