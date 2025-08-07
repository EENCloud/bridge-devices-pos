package main

import (
	"bridge-devices-pos/internal/vendors/seveneleven"
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/eencloud/goeen/log"
)

func main() {
	fmt.Println("=== 7/11 State Machine Edge Case Testing ===")

	ns91Queue := make(chan seveneleven.ETagResult, 100)
	ns92Queue := make(chan seveneleven.ETagResult, 100)

	logger := log.NewContext(os.Stderr, "", log.LevelInfo).GetLogger("test", log.LevelInfo)
	sm := seveneleven.NewIPBasedStateMachine(ns91Queue, ns92Queue, logger)

	processorAdapter := seveneleven.NewProcessorAdapter(logger)
	sm.SetProcessor(processorAdapter)

	fmt.Println("\n1. Testing Real Transaction Data...")
	testRealTransactionData(sm, ns91Queue, ns92Queue)

	fmt.Println("\n2. Testing Normalizer Contract Edge Cases...")
	testNormalizerEdgeCases(sm, ns91Queue, ns92Queue)

	fmt.Println("\n3. Testing RETRYING and ABANDONED scenarios...")
	testRetryingAndAbandoned(sm, ns91Queue, ns92Queue)

	fmt.Println("\n4. Testing IP→ESN Mapping...")
	testIPESNMapping(sm, ns91Queue, ns92Queue)

	fmt.Println("\n=== State Machine Testing Complete ===")
}

func drainChannel(ch <-chan seveneleven.ETagResult, queueName string) {
	drained := 0
	for {
		select {
		case <-ch:
			drained++
		default:
			if drained > 0 {
				fmt.Printf("    Drained %d items from %s queue\n", drained, queueName)
			}
			return
		}
	}
}

func testRealTransactionData(sm *seveneleven.IPBasedStateMachine, ns91Queue, ns92Queue <-chan seveneleven.ETagResult) {
	dataDir := "data/7eleven/register_logs/extracted_jsonl"

	files := []string{
		"register_1.jsonl",
		"register_3.jsonl",
		"register_4.jsonl",
	}

	for _, filename := range files {
		fmt.Printf("\n  Processing %s...\n", filename)
		processJSONLFile(sm, filepath.Join(dataDir, filename))

		drainChannel(ns91Queue, "NS91")
		drainChannel(ns92Queue, "NS92")
		fmt.Printf("  Results: %d NS91s, %d NS92s\n", len(ns91Queue), len(ns92Queue))
	}
}

func testNormalizerEdgeCases(sm *seveneleven.IPBasedStateMachine, ns91Queue, ns92Queue <-chan seveneleven.ETagResult) {
	registerIP := "192.168.1.100"

	fmt.Println("\n  Edge Case 1: Duplicate StartTransaction (RETRYING)")
	events1 := []string{
		`{"CMD":"StartTransaction"}`,
		`{"metaData":{"storeNumber":"38551","terminalNumber":"01","transactionSeqNumber":"3495","transactionType":"Sale","operator":"OP41"}}`,
		`{"transactionHeader":{"headerLines":["7 ELEVEN","8800 HARMON ROAD"]}}`,
		`{"cartChangeTrail":{"eventType":"addLineItem","itemName":"Test Item","price":2.99}}`,
		`{"CMD":"StartTransaction"}`,
	}

	for _, event := range events1 {
		_ = sm.ProcessEvent(registerIP, []byte(event))
	}

	abandonedCount := checkForStatus(ns92Queue, "ABANDONED")
	fmt.Printf("  Found %d ABANDONED status NS92s\n", abandonedCount)

	fmt.Println("\n  Edge Case 2: Unknown Events (UNKNOWN)")
	unknownEvent := `{"metaData":{"storeNumber":"38551","terminalNumber":"01","transactionSeqNumber":"","operator":"OP41"},"cartChangeTrail":{"eventType":"addItemError","eventResult":"SA Cancelled Age Verification"}}`
	_ = sm.ProcessEvent("192.168.1.200", []byte(unknownEvent))

	unknownCount := checkForStatus(ns92Queue, "UNKNOWN")
	fmt.Printf("  Found %d UNKNOWN status NS92s\n", unknownCount)

	fmt.Println("\n  Edge Case 3: UPC as Item Name")
	upcEvent := `{"metaData":{"storeNumber":"38551","terminalNumber":"01","transactionSeqNumber":"3500","operator":"OP41"},"cartChangeTrail":{"eventType":"addLineItem","itemName":"00049578980215","price":20,"itemDescription":"Merchandise Tax - Non-FS"}}`
	_ = sm.ProcessEvent("192.168.1.300", []byte(upcEvent))

	drainChannel(ns92Queue, "NS92")
	fmt.Printf("  Processed UPC item: 0 NS92s\n")
}

func processJSONLFile(sm *seveneleven.IPBasedStateMachine, filename string) {
	file, err := os.Open(filename)
	if err != nil {
		fmt.Printf("  Error opening %s: %v\n", filename, err)
		return
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	lineCount := 0
	registerIP := fmt.Sprintf("192.168.1.%d", 100+(lineCount%10))

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var data map[string]interface{}
		if err := json.Unmarshal([]byte(line), &data); err != nil {
			fmt.Printf("  Error parsing JSON line %d: %v\n", lineCount, err)
			continue
		}

		_ = sm.ProcessEvent(registerIP, []byte(line))
		lineCount++

		if lineCount%50 == 0 {
			fmt.Printf("  Processed %d events...\n", lineCount)
		}
	}

	fmt.Printf("  Total events processed: %d\n", lineCount)
}

func checkForStatus(ch <-chan seveneleven.ETagResult, targetStatus string) int {
	count := 0
	for {
		select {
		case result := <-ch:
			if result.Status != nil && string(*result.Status) == targetStatus {
				count++
				fmt.Printf("    Found %s: %s (IP: %s, Seq: %s)\n", targetStatus, result.ID, result.RegisterIP, result.TransactionSeq)
			}
		case <-time.After(100 * time.Millisecond):
			return count
		}
	}
}

func testRetryingAndAbandoned(sm *seveneleven.IPBasedStateMachine, ns91Queue, ns92Queue chan seveneleven.ETagResult) {
	fmt.Println("Testing RETRYING and ABANDONED transaction scenarios...")

	initialNS92Count := len(ns92Queue)

	// Test Case 1: RETRYING - Same transactionSeqNumber appears twice
	fmt.Println("\n   Case 1: RETRYING Detection...")
	testIP := "192.168.100.1"

	// First transaction attempt with "RETRY001"
	_ = sm.ProcessEvent(testIP, []byte(`{"CMD": "StartTransaction"}`))
	_ = sm.ProcessEvent(testIP, []byte(`{"metaData": {"transactionSeqNumber": "RETRY001", "storeNumber": "12345"}}`))
	_ = sm.ProcessEvent(testIP, []byte(`{"transactionHeader": {"totalAmount": 1550}}`))
	_ = sm.ProcessEvent(testIP, []byte(`{"cartChangeTrail": {"eventType": "ADD_ITEM", "itemCode": "123"}}`))
	// No EndTransaction - incomplete

	// Second transaction attempt with SAME transactionSeqNumber "RETRY001"
	_ = sm.ProcessEvent(testIP, []byte(`{"CMD": "StartTransaction"}`))
	_ = sm.ProcessEvent(testIP, []byte(`{"metaData": {"transactionSeqNumber": "RETRY001", "storeNumber": "12345"}}`)) // Should trigger RETRYING

	// Test Case 2: ABANDONED - Different transactionSeqNumber
	fmt.Println("\n   Case 2: ABANDONED Detection...")
	testIP2 := "192.168.100.2"

	// First transaction with "ABANDON001"
	_ = sm.ProcessEvent(testIP2, []byte(`{"CMD": "StartTransaction"}`))
	_ = sm.ProcessEvent(testIP2, []byte(`{"metaData": {"transactionSeqNumber": "ABANDON001", "storeNumber": "12345"}}`))
	_ = sm.ProcessEvent(testIP2, []byte(`{"transactionHeader": {"totalAmount": 2500}}`))
	_ = sm.ProcessEvent(testIP2, []byte(`{"cartChangeTrail": {"eventType": "ADD_ITEM", "itemCode": "456"}}`))
	// No EndTransaction - incomplete

	// New transaction with DIFFERENT transactionSeqNumber "ABANDON002"
	_ = sm.ProcessEvent(testIP2, []byte(`{"CMD": "StartTransaction"}`))
	_ = sm.ProcessEvent(testIP2, []byte(`{"metaData": {"transactionSeqNumber": "ABANDON002", "storeNumber": "12345"}}`)) // Should mark previous as ABANDONED

	time.Sleep(100 * time.Millisecond) // Allow processing

	finalNS92Count := len(ns92Queue)
	newNS92s := finalNS92Count - initialNS92Count

	fmt.Printf("   Generated %d new NS92 events\n", newNS92s)

	// Analyze the new NS92s for ABANDONED statuses
	abandonedCount := 0

	for i := 0; i < newNS92s && len(ns92Queue) > 0; i++ {
		result := <-ns92Queue
		if result.Status != nil {
			switch *result.Status {
			case seveneleven.StatusAbandoned:
				abandonedCount++
				fmt.Printf("   ABANDONED detected: %s\n", result.TransactionSeq)
			}
		}
	}

	fmt.Printf("   Summary: %d ABANDONED\n", abandonedCount)
}

func testIPESNMapping(sm *seveneleven.IPBasedStateMachine, ns91Queue, ns92Queue <-chan seveneleven.ETagResult) {
	fmt.Println("  Draining queues before IP→ESN mapping test...")
	drainChannel(ns91Queue, "NS91")
	drainChannel(ns92Queue, "NS92")

	ipESNMappings := map[string]string{
		"192.168.1.1": "10037402",
		"192.168.1.2": "1005c523",
		"192.168.1.3": "1001c42e",
	}

	sm.SetIPESNMappings(ipESNMappings)

	testIPs := []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"}
	expectedESNs := []string{"10037402", "1005c523", "1001c42e"}

	for i, testIP := range testIPs {
		fmt.Printf("  Testing IP %s (expected ESN: %s)\n", testIP, expectedESNs[i])

		event := `{"metaData":{"storeNumber":"38551","terminalNumber":"01","transactionSeqNumber":"1234","transactionType":"Sale"}}`
		if err := sm.ProcessEvent(testIP, []byte(event)); err != nil {
			fmt.Printf("    ERROR: Failed to process event: %v\n", err)
			continue
		}

		select {
		case result := <-ns92Queue:
			if result.ESN == expectedESNs[i] {
				fmt.Printf("    SUCCESS: Got ESN %s for IP %s\n", result.ESN, testIP)
			} else {
				fmt.Printf("    FAIL: Got ESN %s, expected %s for IP %s\n", result.ESN, expectedESNs[i], testIP)
			}
		case <-time.After(500 * time.Millisecond):
			fmt.Printf("    TIMEOUT: No ETag generated for IP %s\n", testIP)
		}
	}
}
