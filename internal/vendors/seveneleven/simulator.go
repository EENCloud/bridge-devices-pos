package seveneleven

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eencloud/goeen/log"
)

type Simulator struct {
	logger          *log.Logger
	targetURL       string
	dataDir         string
	trafficIPs      []string
	ipToTerminal    map[string]string
	ipToStoreNumber map[string]string
	seqMutex        sync.Mutex
	seqToFull       map[string]string
	stopChan        chan struct{}
	stopped         bool
	stopMutex       sync.Mutex
	blockedIPs      map[string]bool
	blockMutex      sync.RWMutex
}

func NewSimulator(logger *log.Logger, targetURL string, trafficIPs []string) *Simulator {
	// Load config to get terminal numbers
	ipToTerminal, ipToStoreNumber := loadSimulatorConfig(logger)

	return &Simulator{
		logger:          logger,
		targetURL:       targetURL,
		dataDir:         "data/7eleven/register_logs/extracted_jsonl",
		trafficIPs:      trafficIPs,
		ipToTerminal:    ipToTerminal,
		ipToStoreNumber: ipToStoreNumber,
		seqMutex:        sync.Mutex{},
		seqToFull:       make(map[string]string),
		stopChan:        make(chan struct{}),
		stopped:         false,
		blockedIPs:      make(map[string]bool),
		blockMutex:      sync.RWMutex{},
	}
}

func (s *Simulator) Start() error {
	s.stopMutex.Lock()
	if s.stopped {
		s.stopMutex.Unlock()
		return fmt.Errorf("simulator already stopped")
	}
	s.stopMutex.Unlock()

	s.logger.Info("Starting 7-Eleven simulator...")

	// FIVE official register files - each IP cycles through ALL of them
	registerFiles := []string{
		"register_1.jsonl", "register_2.jsonl", "register_3.jsonl",
		"register_4.jsonl", "register_5.jsonl",
	}

	for _, sourceIP := range s.trafficIPs {
		// Skip blocked startup sim ips - don't generate traffic for them
		if s.isIPBlocked(sourceIP) {
			s.logger.Infof("Skipping traffic generation for blocked startup sim ips: %s", sourceIP)
			continue
		}

		// Each IP cycles through ALL 5 register files
		for _, registerFile := range registerFiles {
			go s.simulateRegister(registerFile, sourceIP)
		}
	}

	// Wait for stop signal instead of blocking forever
	<-s.stopChan
	s.logger.Info("Simulator stopped")
	return nil
}

func (s *Simulator) simulateRegister(filename, sourceIP string) {
	filePath := filepath.Join(s.dataDir, filename)
	s.logger.Infof("Starting simulation for %s from IP %s", filename, sourceIP)

	file, err := os.Open(filePath)
	if err != nil {
		s.logger.Errorf("Failed to open %s: %v", filename, err)
		return
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	lineCount := 0

	for scanner.Scan() {
		// Check if we should stop
		select {
		case <-s.stopChan:
			s.logger.Infof("Stopping simulation for %s from IP %s", filename, sourceIP)
			return
		default:
		}

		line := scanner.Text()
		if line == "" {
			continue
		}

		if err := s.postJSON(line, sourceIP); err != nil {
			s.logger.Errorf("Failed to post JSON from %s: %v", filename, err)
		} else {
			lineCount++
			if lineCount%10 == 0 {
				s.logger.Infof("%s: Posted %d JSON events", filename, lineCount)
			}
		}

		// Sleep with early exit on stop signal
		select {
		case <-s.stopChan:
			s.logger.Infof("Stopping simulation for %s from IP %s", filename, sourceIP)
			return
		case <-time.After(2000 * time.Millisecond):
		}
	}

	if err := scanner.Err(); err != nil {
		s.logger.Errorf("Error reading %s: %v", filename, err)
	}

	s.logger.Infof("Completed simulation for %s - %d events posted", filename, lineCount)
}

func (s *Simulator) postJSON(jsonData, sourceIP string) error {
	// Check if this IP is blocked from traffic generation
	if s.isIPBlocked(sourceIP) {
		s.logger.Infof("Skipping traffic generation for blocked startup sim ips: %s", sourceIP)
		return nil
	}

	maxRetries := 5
	initialDelay := 100 * time.Millisecond

	// Parse JSON to check for transactionSeqNumber and update timestamps
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(jsonData), &data); err == nil {
		// Check if this has metadata with transactionSeqNumber
		if metaData, ok := data["metaData"].(map[string]interface{}); ok {
			// Override terminal number and store number based on IP
			if terminalNumber, exists := s.ipToTerminal[sourceIP]; exists {
				metaData["terminalNumber"] = terminalNumber
			}
			if storeNumber, exists := s.ipToStoreNumber[sourceIP]; exists {
				metaData["storeNumber"] = storeNumber
			}
			if txnSeq, exists := metaData["transactionSeqNumber"].(string); exists && txnSeq != "" {
				s.seqMutex.Lock()
				if fullSeq, exists := s.seqToFull[txnSeq]; exists {
					// Use the same full sequence as before for this base sequence
					metaData["transactionSeqNumber"] = fullSeq
				} else {
					// Generate unique sequence: original-timestamp-random
					now := time.Now().Unix()
					random := rand.Intn(65536) // 16-bit random number (0-65535)
					fullSeq := fmt.Sprintf("%s-%x-%x", txnSeq, now, random)
					s.seqToFull[txnSeq] = fullSeq
					metaData["transactionSeqNumber"] = fullSeq
				}
				s.seqMutex.Unlock()
			}
		}

		// Always update ALL timestamp fields to current time (recursive search)
		s.updateAllTimestamps(data)

		// Re-marshal the modified JSON
		if modifiedJSON, err := json.Marshal(data); err == nil {
			jsonData = string(modifiedJSON)
		}
	}

	targetURL := s.targetURL + "/" + sourceIP

	for i := 0; i < maxRetries; i++ {
		resp, err := http.Post(targetURL, "application/json", bytes.NewBufferString(jsonData))
		if err == nil {
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
			}
			return nil
		}

		s.logger.Errorf("Attempt %d to post JSON failed: %v. Retrying in %v...", i+1, err, initialDelay*(time.Duration(1<<i)))
		time.Sleep(initialDelay * (time.Duration(1 << i)))
	}
	return fmt.Errorf("failed to post JSON after %d retries", maxRetries)
}

func (s *Simulator) updateAllTimestamps(data interface{}) {
	switch v := data.(type) {
	case map[string]interface{}:
		for k, val := range v {
			if k == "timeStamp" {
				if _, isString := val.(string); isString {
					now := time.Now()
					v[k] = now.Format("2006-01-02T15:04:05")
				}
			} else {
				s.updateAllTimestamps(val)
			}
		}
	case []interface{}:
		for _, val := range v {
			s.updateAllTimestamps(val)
		}
	}
}

func loadSimulatorConfig(logger *log.Logger) (map[string]string, map[string]string) {
	ipToTerminal := make(map[string]string)
	ipToStoreNumber := make(map[string]string)

	configPath := filepath.Join("data", "7eleven", "sim-vendor-config.json")
	configData, err := os.ReadFile(configPath)
	if err != nil {
		logger.Errorf("Failed to read sim-vendor-config.json: %v", err)
		return ipToTerminal, ipToStoreNumber
	}

	var config struct {
		Registers []struct {
			StoreNumber    string `json:"store_number"`
			TerminalNumber string `json:"terminal_number"`
			IPAddress      string `json:"ip_address"`
		} `json:"registers"`
	}

	if err := json.Unmarshal(configData, &config); err != nil {
		logger.Errorf("Failed to parse sim-vendor-config.json: %v", err)
		return ipToTerminal, ipToStoreNumber
	}

	for _, reg := range config.Registers {
		ipToTerminal[reg.IPAddress] = reg.TerminalNumber
		ipToStoreNumber[reg.IPAddress] = reg.StoreNumber
		logger.Infof("Loaded simulator config: IP %s → Terminal %s, Store %s",
			reg.IPAddress, reg.TerminalNumber, reg.StoreNumber)
	}

	return ipToTerminal, ipToStoreNumber
}

// BlockStaticIPs blocks traffic generation from static baseline IPs
func (s *Simulator) BlockStaticIPs(staticIPs []string) {
	s.blockMutex.Lock()
	defer s.blockMutex.Unlock()

	for _, ip := range staticIPs {
		s.blockedIPs[ip] = true
	}
}

// UnblockAllIPs clears all blocked IPs
func (s *Simulator) UnblockAllIPs() {
	s.blockMutex.Lock()
	defer s.blockMutex.Unlock()

	s.blockedIPs = make(map[string]bool)
}

// isIPBlocked checks if an IP is blocked from traffic generation
func (s *Simulator) isIPBlocked(ip string) bool {
	s.blockMutex.RLock()
	defer s.blockMutex.RUnlock()

	return s.blockedIPs[ip]
}

// Stop gracefully stops the simulator
func (s *Simulator) Stop() {
	s.stopMutex.Lock()
	defer s.stopMutex.Unlock()

	if s.stopped {
		return
	}

	s.logger.Info("Stopping simulator...")
	s.stopped = true
	close(s.stopChan)
}

// DecodeTransactionTimestamp extracts the timestamp from a generated transaction sequence
// Format: "original-timestamp-random" -> "3475-65a1b2c3-a1b2"
func DecodeTransactionTimestamp(txnSeq string) (time.Time, error) {
	parts := strings.Split(txnSeq, "-")
	if len(parts) < 2 {
		return time.Time{}, fmt.Errorf("invalid transaction sequence format: %s", txnSeq)
	}

	timestampHex := parts[1]
	timestamp, err := strconv.ParseInt(timestampHex, 16, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse timestamp from %s: %v", timestampHex, err)
	}

	return time.Unix(timestamp, 0), nil
}
