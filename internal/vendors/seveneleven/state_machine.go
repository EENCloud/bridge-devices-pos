package seveneleven

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/eencloud/goeen/log"
	"github.com/google/uuid"
)

// Forward declaration for processor interface
type ANNTProcessor interface {
	CreateANNTStructure(payload map[string]interface{}, state *TransactionState, namespace int) ([]byte, error)
}

// POS namespace UUID for deterministic UUIDv5 generation

type TransactionStatus string

const (
	StatusUnknown   TransactionStatus = "UNKNOWN"
	StatusAbandoned TransactionStatus = "ABANDONED"
)

type ETagResult struct {
	ID             string
	RegisterIP     string
	TransactionSeq string
	ANNTData       []byte // JSON ANNT structure, not binary ETag
	Namespace      int
	Status         *TransactionStatus
	CreatedAt      time.Time
	ESN            string
}

type POSTransactionState struct {
	TransactionSeq              string
	RegisterIP                  string
	StartedAt                   time.Time
	LastActivity                time.Time
	IsComplete                  bool
	HasStartTriad               bool
	IndividualNS92Allowed       bool // True after happy start triad confirmed
	CMD                         map[string]interface{}
	MetaData                    map[string]interface{}
	TransactionHeader           map[string]interface{}
	EndCMD                      string
	OtherEvents                 []map[string]interface{} // ALL other event types (generic) - includes transactionFooter
	PendingEventsForAggregation []map[string]interface{} // Events waiting for happy start triad
}

type UnknownEventGroup struct {
	RegisterIP   string
	Events       []map[string]interface{}
	FirstSeen    time.Time
	LastActivity time.Time
	WindowKey    string
}

type IPState struct {
	RegisterIP              string
	ActiveTransactions      map[string]*POSTransactionState
	UnknownEventGroups      map[string]*UnknownEventGroup
	PendingStartTransaction *map[string]interface{}
	LastActivity            time.Time
	mutex                   sync.RWMutex
}

type IPBasedStateMachine struct {
	ipStates map[string]*IPState
	mutex    sync.RWMutex

	ns91Queue     chan<- ETagResult
	ns92Queue     chan<- ETagResult
	processor     ANNTProcessor
	esn           string
	ipToESN       map[string]string
	staticMapping map[string]string // Static mappings that take precedence
	logger        *log.Logger
}

func NewIPBasedStateMachine(ns91Queue, ns92Queue chan<- ETagResult, logger *log.Logger) *IPBasedStateMachine {
	return &IPBasedStateMachine{
		ipStates:  make(map[string]*IPState),
		ns91Queue: ns91Queue,
		ns92Queue: ns92Queue,
		esn:       DefaultESN,
		ipToESN:   make(map[string]string),
		logger:    logger,
	}
}

func (sm *IPBasedStateMachine) SetProcessor(processor ANNTProcessor) {
	sm.processor = processor
}

// SetESN sets the ESN for ETag generation
func (sm *IPBasedStateMachine) SetESN(esn string) {
	sm.esn = esn
}

func (sm *IPBasedStateMachine) SetIPESNMappings(mappings map[string]string) {
	// In normal mode, just set the mappings directly
	if sm.ipToESN == nil {
		sm.ipToESN = make(map[string]string)
	}

	// Clear existing mappings and set new ones
	sm.ipToESN = make(map[string]string)
	for ip, esn := range mappings {
		sm.ipToESN[ip] = esn
	}
}

// SetIPESNMappingsReplaceStatic replaces all mappings including static ones (for real configs in sim mode)
func (sm *IPBasedStateMachine) SetIPESNMappingsReplaceStatic(mappings map[string]string) {
	if sm.ipToESN == nil {
		sm.ipToESN = make(map[string]string)
	}

	// Clear static mappings when real configs arrive
	sm.staticMapping = nil

	// Replace all mappings with new ones
	sm.ipToESN = make(map[string]string)
	for ip, esn := range mappings {
		sm.ipToESN[ip] = esn
	}

	sm.logger.Info("Replaced static mappings with real config mappings")
}

// SetStaticIPESNMappings sets static mappings that take precedence over dynamic ones
func (sm *IPBasedStateMachine) SetStaticIPESNMappings(mappings map[string]string) {
	sm.staticMapping = make(map[string]string)
	for ip, esn := range mappings {
		sm.staticMapping[ip] = esn
	}

	// Update the active mappings to include static ones
	if sm.ipToESN == nil {
		sm.ipToESN = make(map[string]string)
	}
	for ip, esn := range mappings {
		sm.ipToESN[ip] = esn
	}
}

func (sm *IPBasedStateMachine) AddIPESNMapping(ip, esn string) {
	if sm.ipToESN == nil {
		sm.ipToESN = make(map[string]string)
	}

	// Dynamic configs can override static mappings (simulation baseline behavior)
	sm.ipToESN[ip] = esn
	sm.logger.Infof("Added ESN %s for IP %s", esn, ip)
}

// ClearStaticMappings removes static baseline mappings (called when first real config arrives)
func (sm *IPBasedStateMachine) ClearStaticMappings() {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	if sm.staticMapping != nil {
		staticCount := len(sm.staticMapping)

		// Remove static mappings from active mappings
		for ip := range sm.staticMapping {
			delete(sm.ipToESN, ip)
		}

		// Clear static mapping reference
		sm.staticMapping = nil

		sm.logger.Infof("Cleared %d static baseline mappings - now using only real configs", staticCount)
	}
}

// GetIPESNMappings returns a copy of the current IP-to-ESN mappings
func (sm *IPBasedStateMachine) GetIPESNMappings() map[string]string {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()

	if sm.ipToESN == nil {
		return make(map[string]string)
	}

	// Return a copy to prevent external modification
	mappings := make(map[string]string)
	for ip, esn := range sm.ipToESN {
		mappings[ip] = esn
	}
	return mappings
}

func (sm *IPBasedStateMachine) getESNForIP(ip string) string {
	if sm.ipToESN != nil {
		if esn, exists := sm.ipToESN[ip]; exists {
			if isValidESN(esn) {
				return esn
			}
			sm.logger.Errorf("Invalid ESN format for IP %s: %s", ip, esn)
		}
	}

	if sm.esn != "" && isValidESN(sm.esn) {
		return sm.esn
	}

	sm.logger.Errorf("No valid ESN found for IP %s", ip)
	return InvalidESN
}

func isValidESN(esn string) bool {
	if len(esn) != 8 {
		return false
	}
	for _, c := range esn {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (sm *IPBasedStateMachine) ProcessEvent(registerIP string, eventData []byte) error {
	var data map[string]interface{}
	if err := json.Unmarshal(eventData, &data); err != nil {
		return err
	}

	ipState := sm.getOrCreateIPState(registerIP)
	ipState.mutex.Lock()
	defer ipState.mutex.Unlock()

	// Clean up old unknown events and abandoned transactions
	sm.cleanupOldUnknownEvents(ipState)
	sm.cleanupAbandonedTransactions(ipState)

	now := time.Now()
	ipState.LastActivity = now

	// Handle CMD events without transaction sequences (demarcators)
	if cmd, ok := data["CMD"].(string); ok {
		switch cmd {
		case "StartTransaction":
			return sm.handleStartTransaction(ipState, data, now)
		case "EndTransaction", "switchModeToSCO":
			// RULE: Demarcators without transaction context → UNKNOWN (not ABANDONED/RETRYING)
			// Only assign ABANDONED/RETRYING after we have metadata to determine transaction linkage
			return sm.handleEndTransaction(ipState, data, now)
		}
	}

	// Extract transaction sequence - RULE: No txnSeq = check for active transaction first
	transactionSeq := extractTransactionSeq(data)

	if transactionSeq == "" {
		// RULE: Events without transaction sequence numbers should be linked to active transaction if one exists
		// Only treat as unknown event if no active transaction exists for this IP
		if len(ipState.ActiveTransactions) == 1 {
			// Link to the single active transaction
			for seq := range ipState.ActiveTransactions {
				return sm.handleTransactionEvent(ipState, seq, data, now)
			}
		} else if len(ipState.ActiveTransactions) > 1 {
			// Multiple active transactions - link to most recent
			var latestSeq string
			var latestTime time.Time
			for seq, tx := range ipState.ActiveTransactions {
				if latestSeq == "" || tx.LastActivity.After(latestTime) {
					latestSeq = seq
					latestTime = tx.LastActivity
				}
			}
			return sm.handleTransactionEvent(ipState, latestSeq, data, now)
		}
		// No active transactions - treat as unknown event with UNKNOWN status

		return sm.handleUnknownEvent(ipState, data, now)
	}

	// RULE: Events with transaction sequence numbers → linked to transactions
	// Status determination happens when we have enough context (metadata)
	return sm.handleTransactionEvent(ipState, transactionSeq, data, now)
}

func (sm *IPBasedStateMachine) handleStartTransaction(ipState *IPState, eventData map[string]interface{}, now time.Time) error {
	// RULE: When we see a premature next start, we wait until metadata to do ABANDONED or RETRYING on aggregate
	// BUT: We can immediately clean up transactions based on their current state

	for transactionSeq, transaction := range ipState.ActiveTransactions {
		if transaction.HasStartTriad {
			// Transaction with complete start triad → will be ABANDONED when new metadata arrives
			// Send aggregate now with ABANDONED status since this pattern indicates incomplete retry
			status := StatusAbandoned
			sm.sendToNS91(transaction, &status)
			delete(ipState.ActiveTransactions, transactionSeq)
		} else {
			// Incomplete transaction (no happy start triad) → ABANDONED immediately
			// No need to wait for metadata since this transaction never got complete context
			status := StatusAbandoned
			sm.sendToNS91(transaction, &status)
			delete(ipState.ActiveTransactions, transactionSeq)
		}
	}

	// RULE: Pending StartTransaction that never got metadata → ABANDONED
	if ipState.PendingStartTransaction != nil {
		abandonedEvent := map[string]interface{}{
			"_pos": map[string]interface{}{
				"domain":      "711pos2",
				"register_ip": ipState.RegisterIP,
				"events":      []map[string]interface{}{*ipState.PendingStartTransaction},
			},
		}
		status := StatusAbandoned
		sm.sendToNS92(abandonedEvent, ipState.RegisterIP, "", &status)
	}

	// Create new pending start transaction - will be linked when metaData arrives
	ipState.PendingStartTransaction = &map[string]interface{}{
		"CMD":       "StartTransaction",
		"timestamp": now,
	}

	return nil
}

func (sm *IPBasedStateMachine) handleEndTransaction(ipState *IPState, eventData map[string]interface{}, now time.Time) error {
	// RULE: EndTransaction is a sequence demarcator
	// - If it has transaction context (linked to active transaction) → complete/abandon transaction
	// - If it has no transaction context → would be handled as unknown event (UNKNOWN) in ProcessEvent
	// This function only gets called for demarcators that are CMD events without txnSeq

	// RULE: Demarcators flush all accumulated unknown events as NS91 with UNKNOWN status
	for windowKey, group := range ipState.UnknownEventGroups {
		sm.flushUnknownEventGroup(group)
		delete(ipState.UnknownEventGroups, windowKey)
	}

	// Find the most recent active transaction to potentially complete
	var latestTransaction *POSTransactionState
	var latestSeq string

	for seq, transaction := range ipState.ActiveTransactions {
		if latestTransaction == nil || transaction.LastActivity.After(latestTransaction.LastActivity) {
			latestTransaction = transaction
			latestSeq = seq
		}
	}

	// Mark ALL other transactions (not the latest) as ABANDONED
	for seq, transaction := range ipState.ActiveTransactions {
		if seq != latestSeq && !transaction.IsComplete {
			status := StatusAbandoned
			sm.sendToNS91(transaction, &status)
			delete(ipState.ActiveTransactions, seq)
		}
	}

	// RULE: Pending StartTransaction that never got metadata → ABANDONED
	if ipState.PendingStartTransaction != nil {
		abandonedEvent := map[string]interface{}{
			"_pos": map[string]interface{}{
				"domain":      "711pos2",
				"register_ip": ipState.RegisterIP,
				"events":      []map[string]interface{}{*ipState.PendingStartTransaction},
			},
		}
		status := StatusAbandoned
		sm.sendToNS92(abandonedEvent, ipState.RegisterIP, "", &status)
		ipState.PendingStartTransaction = nil
	}

	// Try to complete the latest transaction if it exists
	if latestTransaction != nil {
		// Only send NS91 if happy start triad was confirmed (complete transaction)
		if latestTransaction.IndividualNS92Allowed {
			latestTransaction.IsComplete = true
			if cmd, ok := eventData["CMD"].(string); ok {
				latestTransaction.EndCMD = cmd
			}
			sm.sendToNS91(latestTransaction, nil)
		} else {
			// Incomplete transaction - send aggregate NS91 with ABANDONED status
			status := StatusAbandoned
			sm.sendToNS91(latestTransaction, &status)
		}
		delete(ipState.ActiveTransactions, latestSeq)
	}
	// RULE: If no active transactions exist, this EndTransaction is just a demarcator
	// Demarcators are NEVER sent as individual NS92s - they only trigger flushing actions
	// The unknown events were already flushed above

	return nil
}

func (sm *IPBasedStateMachine) getOrCreateIPState(registerIP string) *IPState {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	if state, exists := sm.ipStates[registerIP]; exists {
		return state
	}

	state := &IPState{
		RegisterIP:              registerIP,
		ActiveTransactions:      make(map[string]*POSTransactionState),
		UnknownEventGroups:      make(map[string]*UnknownEventGroup),
		PendingStartTransaction: nil,
		LastActivity:            time.Now(),
	}
	sm.ipStates[registerIP] = state
	return state
}

func (sm *IPBasedStateMachine) handleTransactionEvent(ipState *IPState, transactionSeq string, eventData map[string]interface{}, now time.Time) error {
	// RULE: Events with transaction sequence numbers get linked to transactions
	// Status determination (RETRYING vs ABANDONED) happens when we have metadata context

	// Check for ABANDONED: if there are other active transactions with different txnSeq
	for otherSeq, otherTransaction := range ipState.ActiveTransactions {
		if otherSeq != transactionSeq && !otherTransaction.IsComplete {
			// Different txnSeq → previous transaction abandoned
			status := StatusAbandoned
			sm.sendAggregateNS92(otherTransaction, &status)
			delete(ipState.ActiveTransactions, otherSeq)
		}
	}

	transaction, exists := ipState.ActiveTransactions[transactionSeq]
	if !exists {
		transaction = &POSTransactionState{
			TransactionSeq:              transactionSeq,
			RegisterIP:                  ipState.RegisterIP,
			StartedAt:                   now,
			LastActivity:                now,
			OtherEvents:                 make([]map[string]interface{}, 0),
			PendingEventsForAggregation: make([]map[string]interface{}, 0),
		}

		// Link pending StartTransaction if available
		if ipState.PendingStartTransaction != nil {
			transaction.CMD = *ipState.PendingStartTransaction
			ipState.PendingStartTransaction = nil
		}

		ipState.ActiveTransactions[transactionSeq] = transaction
	}

	transaction.LastActivity = now

	// RULE: Handle metadata events - this is where we determine duplicate vs new transactions
	if _, ok := eventData["metaData"]; ok {
		// Check for duplicate: if transaction with same sequence already has metadata
		if transaction.MetaData != nil {
			// Same txnSeq seen again → ABANDONED status (register retry failed)
			status := StatusAbandoned
			sm.sendToNS91(transaction, &status)

			// Clear the old transaction and start fresh with same sequence
			delete(ipState.ActiveTransactions, transactionSeq)
			transaction = &POSTransactionState{
				TransactionSeq: transactionSeq,
				RegisterIP:     ipState.RegisterIP,
				StartedAt:      now,
				LastActivity:   now,
				OtherEvents:    make([]map[string]interface{}, 0),
			}

			// Link pending StartTransaction if available
			if ipState.PendingStartTransaction != nil {
				transaction.CMD = *ipState.PendingStartTransaction
				ipState.PendingStartTransaction = nil
			}

			ipState.ActiveTransactions[transactionSeq] = transaction
		} else {
			// Check for ABANDONED: if there are other incomplete transactions on this IP
			for otherSeq, otherTx := range ipState.ActiveTransactions {
				if otherSeq != transactionSeq && otherTx.MetaData != nil && !otherTx.IsComplete {
					// Mark the other transaction as ABANDONED
					status := StatusAbandoned
					sm.sendToNS92(otherTx.MetaData, ipState.RegisterIP, otherSeq, &status)
					delete(ipState.ActiveTransactions, otherSeq)
				}
			}
		}
		transaction.MetaData = eventData
	} else if _, ok := eventData["transactionHeader"]; ok {
		transaction.TransactionHeader = eventData
	} else {
		// Everything else goes into OtherEvents - completely generic for ANY vendor event type
		// This includes transactionFooter, cartChangeTrail, paymentSummary, and ANY unknown types
		transaction.OtherEvents = append(transaction.OtherEvents, eventData)
	}

	// Check for happy start triad completion
	if transaction.CMD != nil && transaction.MetaData != nil && transaction.TransactionHeader != nil && !transaction.HasStartTriad {
		transaction.HasStartTriad = true
		transaction.IndividualNS92Allowed = true // Now allow individual NS92s

		// Send optional Type 1A NS92 (combined start sequence)
		sm.sendCombinedStartTriadNS92(transaction)

		// Process any pending events that were waiting for happy start triad
		for _, pendingEvent := range transaction.PendingEventsForAggregation {
			sm.sendType1BNS92(transaction, pendingEvent)
		}
		transaction.PendingEventsForAggregation = nil // Clear pending events
	}

	// Individual events after happy start triad confirmed
	if transaction.IndividualNS92Allowed {
		// Skip sending individual NS92s for start triad components, completion demarcators, and happy end trio
		skipIndividualNS92 := false
		if cmd, ok := eventData["CMD"].(string); ok {
			// Skip individual NS92 for start/end demarcators - they're handled by aggregate events
			skipIndividualNS92 = (cmd == "StartTransaction" || cmd == "EndTransaction" || cmd == "switchModeToSCO")
		} else if _, ok := eventData["metaData"]; ok {
			skipIndividualNS92 = true // Start triad component
		} else if _, ok := eventData["transactionHeader"]; ok {
			skipIndividualNS92 = true // Start triad component
		} else if _, ok := eventData["paymentSummary"]; ok {
			skipIndividualNS92 = true // Happy end trio - always aggregate in NS91
		} else if _, ok := eventData["transactionSummary"]; ok {
			skipIndividualNS92 = true // Happy end trio - always aggregate in NS91
		} else if _, ok := eventData["transactionFooter"]; ok {
			skipIndividualNS92 = true // Happy end trio - always aggregate in NS91
		}

		if !skipIndividualNS92 {
			// Send Type 1B NS92 for regular transaction events only
			sm.sendType1BNS92(transaction, eventData)
		}
	} else {
		// Happy start triad not yet confirmed - add to pending events for later aggregation
		// BUT skip start triad components (CMD, metaData, transactionHeader)
		if _, isCmd := eventData["CMD"]; !isCmd {
			if _, isMetaData := eventData["metaData"]; !isMetaData {
				if _, isHeader := eventData["transactionHeader"]; !isHeader {
					if transaction.PendingEventsForAggregation == nil {
						transaction.PendingEventsForAggregation = make([]map[string]interface{}, 0)
					}
					transaction.PendingEventsForAggregation = append(transaction.PendingEventsForAggregation, eventData)
				}
			}
		}
	}

	// Check for transaction completion (close demarcators)
	if cmd, ok := eventData["CMD"].(string); ok && (cmd == "EndTransaction" || cmd == "switchModeToSCO") {
		transaction.IsComplete = true
		transaction.EndCMD = cmd

		// Only send NS91 if happy start triad was confirmed (complete transaction)
		if transaction.IndividualNS92Allowed {
			sm.sendToNS91(transaction, nil)
		} else {
			// Incomplete transaction - send aggregate NS91 with ABANDONED status
			status := StatusAbandoned
			sm.sendToNS91(transaction, &status)
		}
		delete(ipState.ActiveTransactions, transactionSeq)
	} else if sm.isTransactionComplete(eventData) {
		transaction.IsComplete = true
		transaction.EndCMD = "Completed"

		if transaction.IndividualNS92Allowed {
			sm.sendToNS91(transaction, nil)
		} else {
			status := StatusAbandoned
			sm.sendToNS91(transaction, &status)
		}
		delete(ipState.ActiveTransactions, transactionSeq)
	}

	return nil
}

func (sm *IPBasedStateMachine) handleUnknownEvent(ipState *IPState, eventData map[string]interface{}, now time.Time) error {

	// RULE: Events without transaction sequence numbers → UNKNOWN status
	// These include: age verification failures, system errors, random events between transactions

	windowKey := fmt.Sprintf("%d", now.Unix()/30)

	group, exists := ipState.UnknownEventGroups[windowKey]
	if !exists {
		group = &UnknownEventGroup{
			RegisterIP:   ipState.RegisterIP,
			Events:       make([]map[string]interface{}, 0),
			FirstSeen:    now,
			LastActivity: now,
			WindowKey:    windowKey,
		}
		ipState.UnknownEventGroups[windowKey] = group
	}

	group.Events = append(group.Events, eventData)
	group.LastActivity = now

	// RULE: Unknown events accumulate and are sent as NS91 with UNKNOWN status when demarcator hits
	// NO individual NS92s for unknown events - they are waste

	if sm.isUnknownEventGroupComplete(group) {
		sm.flushUnknownEventGroup(group)
		delete(ipState.UnknownEventGroups, windowKey)
	}

	return nil
}

func (sm *IPBasedStateMachine) cleanupOldUnknownEvents(ipState *IPState) {
	cutoff := time.Now().Add(-30 * time.Second)
	for windowKey, group := range ipState.UnknownEventGroups {
		if group.LastActivity.Before(cutoff) {
			sm.flushUnknownEventGroup(group)
			delete(ipState.UnknownEventGroups, windowKey)
		}
	}
}

func (sm *IPBasedStateMachine) cleanupAbandonedTransactions(ipState *IPState) {
	cutoff := time.Now().Add(-2 * time.Minute) // 2 minute timeout for abandoned transactions
	for transactionSeq, transaction := range ipState.ActiveTransactions {
		if transaction.LastActivity.Before(cutoff) && !transaction.IsComplete {
			// Send ABANDONED status NS92 - if we tracked it long enough to timeout, it's worth reporting
			status := StatusAbandoned
			abandonedEvent := map[string]interface{}{
				"transactionSeqNumber": transactionSeq,
				"events":               sm.collectTransactionEvents(transaction),
			}
			sm.sendToNS92(abandonedEvent, ipState.RegisterIP, transactionSeq, &status)
			delete(ipState.ActiveTransactions, transactionSeq)
		}
	}
}

func (sm *IPBasedStateMachine) collectTransactionEvents(transaction *POSTransactionState) []map[string]interface{} {
	var events []map[string]interface{}
	if transaction.CMD != nil {
		events = append(events, transaction.CMD)
	}
	if transaction.MetaData != nil {
		events = append(events, transaction.MetaData)
	}
	if transaction.TransactionHeader != nil {
		events = append(events, transaction.TransactionHeader)
	}
	// Add ALL other events generically - no hardcoded event types (includes transactionFooter)
	events = append(events, transaction.OtherEvents...)
	return events
}

func (sm *IPBasedStateMachine) isTransactionComplete(eventData map[string]interface{}) bool {
	// ONLY explicit demarcators (CMD) indicate transaction completion
	// transactionFooter with status is just metadata, NOT a completion trigger
	if cmd, ok := eventData["CMD"].(string); ok {
		return cmd == "EndTransaction" || cmd == "switchModeToSCO"
	}

	return false
}

func (sm *IPBasedStateMachine) isUnknownEventGroupComplete(group *UnknownEventGroup) bool {
	hasMetaData := false
	hasCartChange := false

	for _, event := range group.Events {
		if _, ok := event["metaData"]; ok {
			hasMetaData = true
		}
		if _, ok := event["cartChangeTrail"]; ok {
			hasCartChange = true
		}
	}

	return hasMetaData && hasCartChange
}

func (sm *IPBasedStateMachine) flushUnknownEventGroup(group *UnknownEventGroup) {
	// RULE: All unknown event groups get UNKNOWN status as NS91 - these are events without transaction context
	// NO NS92s for unknown events - they are waste!
	status := StatusUnknown

	// Create a fake transaction for unknown events to send as NS91
	fakeTransaction := &POSTransactionState{
		RegisterIP:     group.RegisterIP,
		TransactionSeq: "", // No sequence for unknown events
		OtherEvents:    group.Events,
	}

	sm.sendToNS91(fakeTransaction, &status)
}

func (sm *IPBasedStateMachine) sendToNS91(transaction *POSTransactionState, status *TransactionStatus) {
	posData := map[string]interface{}{
		"domain":      "711pos2",
		"register_ip": transaction.RegisterIP,
	}

	// Only add CMD if we have an actual end command (not for unknown event aggregates)
	if transaction.EndCMD != "" {
		posData["CMD"] = transaction.EndCMD
	}

	// Safely add metaData if available
	if transaction.MetaData != nil {
		if metaData, ok := transaction.MetaData["metaData"]; ok && metaData != nil {
			posData["metaData"] = metaData
		}
	}

	// Safely add transactionHeader if available
	if transaction.TransactionHeader != nil {
		if header, ok := transaction.TransactionHeader["transactionHeader"]; ok && header != nil {
			posData["transactionHeader"] = header
		}
	}

	// Add ALL other events generically with NS91 aggregation rules
	for _, event := range transaction.OtherEvents {
		for key, value := range event {
			if key != "metaData" && key != "transactionHeader" && key != "CMD" {
				if existingValue, exists := posData[key]; exists {
					// Multiple values - handle based on simplified rules
					if existingArray, isArray := existingValue.([]interface{}); isArray {
						// Already an array, append new value
						posData[key] = append(existingArray, value)
					} else {
						// Convert to array: objects become [{}, {}], arrays become [[], []]
						posData[key] = []interface{}{existingValue, value}
					}
				} else {
					// First occurrence - apply simplified rules
					if valueArray, isArray := value.([]interface{}); isArray {
						// Arrays always get double-wrapped [[...]] for NS91
						posData[key] = []interface{}{valueArray}
					} else {
						// Objects stay as-is {...} for NS91 when single
						posData[key] = value
					}
				}
			}
		}
	}

	// Add status if this is an incomplete transaction
	if status != nil {
		posData["status"] = string(*status)
	}

	completeTransaction := map[string]interface{}{
		"_pos": posData,
	}

	etag := ETagResult{
		ID:             sm.GenerateETagIDFromEvent(transaction.RegisterIP, transaction.TransactionSeq, transaction.MetaData),
		RegisterIP:     transaction.RegisterIP,
		TransactionSeq: transaction.TransactionSeq,
		Namespace:      91,
		Status:         status,
		CreatedAt:      time.Now(),
		ESN:            sm.getESNForIP(transaction.RegisterIP),
	}

	if sm.processor != nil {
		state := &TransactionState{
			UUID: sm.GenerateETagBinaryUUID(transaction.RegisterIP, transaction.TransactionSeq, transaction.MetaData),
			Seq:  uint16(len(transaction.OtherEvents) + 4),
			ESN:  sm.getESNForIP(transaction.RegisterIP),
		}

		if anntData, err := sm.processor.CreateANNTStructure(completeTransaction, state, 91); err == nil {
			etag.ANNTData = anntData
		}
	}

	select {
	case sm.ns91Queue <- etag:
	default:
	}
}

func (sm *IPBasedStateMachine) sendToNS92(eventData map[string]interface{}, registerIP, transactionSeq string, status *TransactionStatus) {

	etag := ETagResult{
		ID:             sm.GenerateETagIDFromEvent(registerIP, transactionSeq, eventData),
		RegisterIP:     registerIP,
		TransactionSeq: transactionSeq,
		Namespace:      92,
		Status:         status,
		CreatedAt:      time.Now(),
		ESN:            sm.getESNForIP(registerIP),
	}

	if sm.processor != nil {
		state := &TransactionState{
			UUID: sm.GenerateETagBinaryUUID(registerIP, transactionSeq, eventData),
			Seq:  1,
			ESN:  sm.getESNForIP(registerIP),
		}

		if anntData, err := sm.processor.CreateANNTStructure(eventData, state, 92); err == nil {
			etag.ANNTData = anntData
		}
	}

	select {
	case sm.ns92Queue <- etag:
	default:
	}
}

func (sm *IPBasedStateMachine) sendCombinedStartTriadNS92(transaction *POSTransactionState) {
	posData := map[string]interface{}{
		"domain":      "711pos2",
		"register_ip": transaction.RegisterIP,
		"CMD":         "StartTransaction",
	}

	// Safely add metaData if available
	if transaction.MetaData != nil {
		if metaData, ok := transaction.MetaData["metaData"]; ok && metaData != nil {
			posData["metaData"] = metaData
		}
	}

	// Safely add transactionHeader if available
	if transaction.TransactionHeader != nil {
		if header, ok := transaction.TransactionHeader["transactionHeader"]; ok && header != nil {
			posData["transactionHeader"] = header
		}
	}

	combinedEvent := map[string]interface{}{
		"_pos": posData,
	}
	sm.sendToNS92(combinedEvent, transaction.RegisterIP, transaction.TransactionSeq, nil)
}

func (sm *IPBasedStateMachine) sendType1BNS92(transaction *POSTransactionState, eventData map[string]interface{}) {
	posData := map[string]interface{}{
		"domain":      "711pos2",
		"register_ip": transaction.RegisterIP,
	}

	// Reinsert metadata from transaction state (key requirement from 7/11 API v2)
	if transaction.MetaData != nil {
		if metaData, ok := transaction.MetaData["metaData"]; ok && metaData != nil {
			posData["metaData"] = metaData
		}
	}

	// Add the individual event data (cartChangeTrail, paymentSummary, etc.)
	for key, value := range eventData {
		if key != "metaData" { // Don't overwrite the reinserted metadata
			posData[key] = value
		}
	}

	enrichedEvent := map[string]interface{}{
		"_pos": posData,
	}

	sm.sendToNS92(enrichedEvent, transaction.RegisterIP, transaction.TransactionSeq, nil)
}

// sendAggregateNS92 sends a single aggregate NS92 for incomplete transactions
func (sm *IPBasedStateMachine) sendAggregateNS92(transaction *POSTransactionState, status *TransactionStatus) {
	aggregateEvent := map[string]interface{}{
		"_pos": map[string]interface{}{
			"domain":      "711pos2",
			"register_ip": transaction.RegisterIP,
		},
	}

	// Include all events that were part of this incomplete transaction
	events := make([]map[string]interface{}, 0)

	// Add start triad components if present
	if transaction.CMD != nil {
		events = append(events, transaction.CMD)
	}
	if transaction.MetaData != nil {
		events = append(events, transaction.MetaData)
	}
	if transaction.TransactionHeader != nil {
		events = append(events, transaction.TransactionHeader)
	}

	// Add all pending events that never became individual NS92s
	events = append(events, transaction.PendingEventsForAggregation...)

	// Add ALL other accumulated events generically - no hardcoded event types (includes transactionFooter)
	events = append(events, transaction.OtherEvents...)

	// Add metadata context if available for ABANDONED status
	if transaction.MetaData != nil {
		if metaData, ok := transaction.MetaData["metaData"]; ok && metaData != nil {
			aggregateEvent["_pos"].(map[string]interface{})["metaData"] = metaData
		}
	}

	aggregateEvent["_pos"].(map[string]interface{})["events"] = events

	sm.sendToNS92(aggregateEvent, transaction.RegisterIP, transaction.TransactionSeq, status)
}

func extractTransactionSeq(eventData map[string]interface{}) string {
	if metaData, ok := eventData["metaData"].(map[string]interface{}); ok {
		if seq, exists := metaData["transactionSeqNumber"].(string); exists {
			// Trim whitespace and check if non-empty
			trimmed := strings.TrimSpace(seq)
			if trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// GenerateETagIDFromEvent creates UUID for individual events (unknown events, NS92)
func (sm *IPBasedStateMachine) GenerateETagIDFromEvent(registerIP, transactionSeq string, eventData map[string]interface{}) string {
	// Try UUIDv5 if this event has complete metaData
	if metaData, ok := eventData["metaData"].(map[string]interface{}); ok {
		if store, hasStore := metaData["storeNumber"].(string); hasStore && store != "" {
			if terminal, hasTerminal := metaData["terminalNumber"].(string); hasTerminal && terminal != "" {
				if txn, hasTxn := metaData["transactionSeqNumber"].(string); hasTxn && txn != "" {
					// UUIDv5 for events with complete transaction info
					nameString := fmt.Sprintf("%s:%s:%s:%s", registerIP, store, terminal, txn)
					transactionUUID := uuid.NewSHA1(posNamespaceUUID, []byte(nameString))
					return transactionUUID.String()
				}
			}
		}
	}

	// UUIDv4 for unknown events and incomplete events
	return uuid.New().String()
}

// GenerateETagBinaryUUID creates the UUID for the ETag header (binary field)
// This can be the same logic but returns the actual UUID for binary embedding
func (sm *IPBasedStateMachine) GenerateETagBinaryUUID(registerIP, transactionSeq string, eventData map[string]interface{}) uuid.UUID {
	// Try UUIDv5 if this event has complete metaData
	if metaData, ok := eventData["metaData"].(map[string]interface{}); ok {
		if store, hasStore := metaData["storeNumber"].(string); hasStore && store != "" {
			if terminal, hasTerminal := metaData["terminalNumber"].(string); hasTerminal && terminal != "" {
				if txn, hasTxn := metaData["transactionSeqNumber"].(string); hasTxn && txn != "" {
					// UUIDv5 for deterministic linking
					nameString := fmt.Sprintf("%s:%s:%s:%s", registerIP, store, terminal, txn)
					return uuid.NewSHA1(posNamespaceUUID, []byte(nameString))
				}
			}
		}
	}

	// UUIDv4 for unknown events
	return uuid.New()
}
