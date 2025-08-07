package seveneleven

import (
	"bridge-devices-pos/internal/core"
	"bridge-devices-pos/internal/settings"
	"bridge-devices-pos/internal/vendors"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"bytes"

	goeen_log "github.com/eencloud/goeen/log"
)

const VendorName = "7eleven"

// LogEndpointPayload shows what was actually served to endpoints
func LogEndpointPayload(result ETagResult) string {
	if len(result.ANNTData) == 0 {
		return "EMPTY_PAYLOAD"
	}

	if json.Valid(result.ANNTData) {
		var prettyJSON bytes.Buffer
		if err := json.Indent(&prettyJSON, result.ANNTData, "", "  "); err == nil {
			return prettyJSON.String()
		}
	}

	return fmt.Sprintf("INVALID_ANNT_FORMAT (%d bytes)", len(result.ANNTData))
}

// LogStorageCompact creates brief storage confirmation
func LogStorageCompact(result ETagResult) string {
	status := ""
	if result.Status != nil {
		status = fmt.Sprintf(" status:%s", string(*result.Status))
	}

	txnInfo := ""
	if result.TransactionSeq != "" {
		txnInfo = fmt.Sprintf(" txn:%s", result.TransactionSeq)
	}

	return fmt.Sprintf("[NS%d] %s%s%s %db",
		result.Namespace, result.RegisterIP, status, txnInfo, len(result.ANNTData))
}

type Settings struct {
	Brand      string          `json:"brand"`
	ListenPort int             `json:"listen_port"`
	Registers  json.RawMessage `json:"registers"`
}

func init() {
	vendors.Register(VendorName, New)
}

type Vendor struct {
	logger       *goeen_log.Logger
	settings     Settings
	ingestor     *Ingestor
	processor    *Processor
	simulator    *Simulator
	anntStore    *core.ANNTStore
	stateMachine *IPBasedStateMachine
	ns91Queue    chan ETagResult
	ns92Queue    chan ETagResult
	currentIPs   []string

	isSimMode      bool
	hasRealConfigs bool // Track if real configs have replaced static baseline

	staticBaselineIPs []string // Track original startup sim ips for blocking
}

func New(logger *goeen_log.Logger, rawConfig json.RawMessage, anntStore *core.ANNTStore) (vendors.Vendor, error) {
	var s Settings
	if err := json.Unmarshal(rawConfig, &s); err != nil {
		return nil, err
	}

	// Create unbounded channels for maximum throughput
	ns91Queue := make(chan ETagResult, 50000) // Large but not unlimited for memory safety
	ns92Queue := make(chan ETagResult, 50000) // Large but not unlimited for memory safety
	stateMachine := NewIPBasedStateMachine(ns91Queue, ns92Queue, logger)

	// Create processor and set it on the state machine
	processor := NewProcessor(logger)
	processorAdapter := NewProcessorAdapter(logger)
	stateMachine.SetProcessor(processorAdapter)
	isSimMode := os.Getenv("MODE") == "simulation"
	ipESNMappings := loadIPESNMappings(logger, isSimMode)

	if isSimMode && len(ipESNMappings) > 0 {
		logger.Info("Setting startup sim ips-ESN mappings for simulation mode")
		stateMachine.SetStaticIPESNMappings(ipESNMappings)
	} else {
		stateMachine.SetIPESNMappings(ipESNMappings)
	}

	// Only set fallback ESN if no mappings exist
	if len(ipESNMappings) == 0 {
		logger.Warning("No IP-ESN mappings found, using fallback ESN")
		stateMachine.SetESN(DefaultESN)
	} else {
		logger.Infof("Using IP-ESN mappings for %d IPs, no fallback ESN needed", len(ipESNMappings))
	}

	return &Vendor{
		logger:            logger,
		settings:          s,
		processor:         processor,
		anntStore:         anntStore,
		stateMachine:      stateMachine,
		ns91Queue:         ns91Queue,
		ns92Queue:         ns92Queue,
		currentIPs:        []string{},
		isSimMode:         isSimMode,
		hasRealConfigs:    false,
		staticBaselineIPs: loadTrafficIPs(logger), // Store original startup sim ips
	}, nil
}

func (v *Vendor) Name() string {
	return VendorName
}

func (v *Vendor) GetIPESNMappings() map[string]string {
	if v.stateMachine == nil {
		return make(map[string]string)
	}
	return v.stateMachine.GetIPESNMappings()
}

func (v *Vendor) GetResolvedConfig() map[string]interface{} {
	result := make(map[string]interface{})
	result["vendor"] = VendorName
	result["listen_port"] = v.settings.ListenPort
	if v.isSimMode {
		result["mode"] = "simulation"
	} else {
		result["mode"] = "production"
	}
	return result
}

func (v *Vendor) HandleESNConfiguration(esn string, vendor *settings.VendorConfig) {
	v.logger.Infof("Handling configuration update from ESN %s", esn)
	v.logger.Infof("Raw registers JSON for ESN %s: %s", esn, string(vendor.Registers))

	var registers []RegisterConfig
	if err := json.Unmarshal(vendor.Registers, &registers); err != nil {
		v.logger.Errorf("Failed to parse registers for ESN %s: %v", esn, err)
		return
	}

	v.logger.Infof("Successfully parsed %d registers for ESN %s", len(registers), esn)

	if v.ingestor != nil {
		v.ingestor.AddESNRegisters(esn, registers)
		for _, reg := range registers {
			v.stateMachine.AddIPESNMapping(reg.IPAddress, esn)
			v.logger.Infof("Updated state machine: IP %s → ESN %s", reg.IPAddress, esn)
		}
	}

	if v.isSimMode {
		// Check if ANY incoming IP is not in static baseline
		staticBaselineIPs := []string{"172.31.99.1", "172.31.99.2"}
		hasRealIP := false

		for _, reg := range registers {
			isStatic := false
			for _, staticIP := range staticBaselineIPs {
				if reg.IPAddress == staticIP {
					isStatic = true
					break
				}
			}
			if !isStatic {
				hasRealIP = true
				v.logger.Infof("Real config detected: %s (not in static baseline)", reg.IPAddress)
				break
			}
		}

		// If ANY real IP detected, block static baseline IPs immediately
		if hasRealIP && v.simulator != nil {
			v.simulator.BlockStaticIPs(staticBaselineIPs)
			v.logger.Infof("Blocked static baseline IPs: %v", staticBaselineIPs)
		} else {
			v.logger.Infof("Config from ESN %s contains only static baseline IPs - ignoring for simulator restart", esn)
		}
	}
}

func (v *Vendor) Start() error {
	if v.isSimMode {
		v.logger.Info("Simulation mode: Starting simulator with baseline IPs - will switch to real configs when available")
		v.settings.ListenPort = 6324
		// Start with static baseline IPs for initial traffic generation
		trafficIPs := loadTrafficIPs(v.logger)
		v.currentIPs = trafficIPs
		v.simulator = NewSimulator(v.logger, "http://localhost:6324", trafficIPs)
		go func() {
			if err := v.simulator.Start(); err != nil {
				v.logger.Errorf("7-Eleven simulator failed: %v", err)
			}
		}()
	} else {
		v.logger.Info("Non-simulation mode: Waiting for external test scripts")
	}

	v.logger.Infof("Starting 7-Eleven vendor services on port %d (verbose: %s)...",
		v.settings.ListenPort, os.Getenv("POS_VERBOSE_LOGGING"))
	// For now, create a placeholder channel until we refactor ingestor
	placeholderQueue := make(chan []byte, 1000)
	v.ingestor = NewIngestor(v.logger, v.settings, v.processor, placeholderQueue, v.stateMachine)

	go func() {
		if err := v.ingestor.Start(); err != nil {
			v.logger.Errorf("7-Eleven ingestor failed: %v", err)
		}
	}()

	go v.processStateResults()

	return nil
}

func (v *Vendor) processStateResults() {
	for {
		select {
		case result := <-v.ns91Queue:
			// Convert ETagResult to ANNTItem
			var anntData map[string]interface{}
			if result.ANNTData != nil {
				if err := json.Unmarshal(result.ANNTData, &anntData); err != nil {
					v.logger.Errorf("Failed to unmarshal ANNTData to ANNT: %v", err)
					continue
				}

			}

			anntItem := core.ANNTItem{
				ID:             result.ID,
				RegisterIP:     result.RegisterIP,
				TransactionSeq: result.TransactionSeq,
				ANNTData:       anntData,
				Namespace:      result.Namespace,
				Priority:       1,
				CreatedAt:      result.CreatedAt,
				ESN:            result.ESN,
			}

			if err := v.anntStore.StoreANNT(anntItem); err != nil {
				v.logger.Errorf("Failed to store NS91 ANNT: %v", err)
			} else if v.isSimMode {
				v.logger.Infof("NS91 Stored: %s", LogStorageCompact(result))
			}

		case result := <-v.ns92Queue:
			if v.isSimMode {
				if os.Getenv("POS_VERBOSE_LOGGING") == "true" {
					v.logger.Infof("NS92 Sent to Bridge - Payload:\n%s", LogEndpointPayload(result))
				} else {
					v.logger.Infof("NS92 Sent to Bridge: %s", LogStorageCompact(result))
				}
			}

			// Convert ETagResult to ANNTItem
			var anntData map[string]interface{}
			if result.ANNTData != nil {
				if err := json.Unmarshal(result.ANNTData, &anntData); err != nil {
					v.logger.Errorf("Failed to unmarshal ANNTData to ANNT: %v", err)
					continue
				}

			}

			anntItem := core.ANNTItem{
				ID:             result.ID,
				RegisterIP:     result.RegisterIP,
				TransactionSeq: result.TransactionSeq,
				ANNTData:       anntData,
				Namespace:      result.Namespace,
				Priority:       1,
				CreatedAt:      result.CreatedAt,
				ESN:            result.ESN,
			}

			if err := v.anntStore.StoreANNT(anntItem); err != nil {
				v.logger.Errorf("Failed to store NS92 ANNT: %v", err)
			} else if v.isSimMode {
				v.logger.Infof("NS92 Stored: %s", LogStorageCompact(result))
			}
		}
	}
}

func (v *Vendor) Stop(ctx context.Context) error {
	if v.simulator != nil {
		v.simulator.Stop()
	}
	if v.ingestor == nil {
		return nil
	}
	v.logger.Info("Stopping 7-Eleven vendor services...")
	return v.ingestor.Stop(ctx)
}

func loadIPESNMappings(logger *goeen_log.Logger, isSimMode bool) map[string]string {
	if isSimMode {
		logger.Info("Simulation mode: Loading startup sim ips-ESN mappings from sim-vendor-config.json")
		configPath := filepath.Join("data", "7eleven", "sim-vendor-config.json")
		configData, err := os.ReadFile(configPath)
		if err != nil {
			logger.Errorf("Failed to read config: %v", err)
			return make(map[string]string)
		}

		var config struct {
			ESNMappings map[string]string `json:"esn_mappings"`
		}

		if err := json.Unmarshal(configData, &config); err != nil {
			logger.Errorf("Failed to parse config: %v", err)
			return make(map[string]string)
		}

		logger.Infof("Loaded %d startup sim ips-ESN mappings", len(config.ESNMappings))
		return config.ESNMappings
	}

	logger.Info("Production mode: IP-ESN mappings will be built dynamically from camera configurations")
	return make(map[string]string)
}

func loadTrafficIPs(logger *goeen_log.Logger) []string {
	configPath := filepath.Join("data", "7eleven", "sim-vendor-config.json")
	configData, err := os.ReadFile(configPath)
	if err != nil {
		logger.Errorf("Failed to read sim-vendor-config.json: %v", err)
		return []string{}
	}

	var config struct {
		Registers []struct {
			IPAddress string `json:"ip_address"`
		} `json:"registers"`
	}

	if err := json.Unmarshal(configData, &config); err != nil {
		logger.Errorf("Failed to parse sim-vendor-config.json: %v", err)
		return []string{}
	}

	var trafficIPs []string
	for _, reg := range config.Registers {
		trafficIPs = append(trafficIPs, reg.IPAddress)
	}

	logger.Infof("Loaded %d traffic IPs for simulation", len(trafficIPs))
	return trafficIPs
}
