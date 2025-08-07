package api

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v4"
)

type incomingDataLog struct {
	Timestamp  time.Time   `json:"timestamp"`
	RemoteAddr string      `json:"remote_addr"`
	Data       interface{} `json:"data"`
	EventType  string      `json:"event_type"`
}

var (
	incomingDataMutex sync.RWMutex
	incomingData      []incomingDataLog
	maxIncomingLogs   = 10000      // Increased from 100 to 10000 for better debugging
	serviceStartTime  = time.Now() // Track service uptime
)

func LogIncomingData(remoteAddr string, data interface{}) {
	incomingDataMutex.Lock()
	defer incomingDataMutex.Unlock()

	eventType := "pos_event"
	if dataMap, ok := data.(map[string]interface{}); ok {
		// Generic event logging - vendor-agnostic
		if cmd, exists := dataMap["CMD"]; exists {
			eventType = fmt.Sprintf("cmd_%s", cmd.(string))
		} else {
			// Count other fields generically without knowing vendor specifics
			eventType = fmt.Sprintf("event_%d_fields", len(dataMap))
		}
	}

	entry := incomingDataLog{
		Timestamp:  time.Now(),
		RemoteAddr: remoteAddr,
		Data:       data,
		EventType:  eventType,
	}

	incomingData = append(incomingData, entry)
	if len(incomingData) > maxIncomingLogs {
		incomingData = incomingData[len(incomingData)-maxIncomingLogs:]
	}
}

func (s *Server) settingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST method is allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract ESN from URL parameter (matches production behavior)
	cameraid := r.URL.Query().Get("cameraid")
	if cameraid == "" {
		http.Error(w, "cameraid parameter required", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.Logger.Errorf("Error reading settings body: %v", err)
		http.Error(w, "Error reading request body", http.StatusInternalServerError)
		return
	}

	// Basic validation and default vendor assignment
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		s.Logger.Errorf("Invalid JSON in settings: %v", err)
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Handle vendor configuration with seveneleven as default
	if vendors, ok := payload["vendors"].([]interface{}); ok {
		if len(vendors) == 0 {
			s.Logger.Warningf("Empty vendors array for ESN %s", cameraid)
		}
		// Auto-default brand to "seveneleven" if missing, validate if present
		for _, v := range vendors {
			if vendor, ok := v.(map[string]interface{}); ok {
				if brand, ok := vendor["brand"].(string); ok {
					if brand != "seveneleven" {
						s.Logger.Errorf("Unknown vendor brand '%s' for ESN %s (only 'seveneleven' supported)", brand, cameraid)
						http.Error(w, fmt.Sprintf("Unknown vendor brand: %s (only 'seveneleven' supported)", brand), http.StatusBadRequest)
						return
					}
				} else {
					// Default to seveneleven if brand not specified
					vendor["brand"] = "seveneleven"
					s.Logger.Infof("Defaulting to 'seveneleven' vendor for ESN %s", cameraid)
				}
			}
		}
		// Re-serialize with defaults applied
		if body, err = json.Marshal(payload); err != nil {
			s.Logger.Errorf("Failed to re-serialize payload with defaults: %v", err)
			http.Error(w, "Internal processing error", http.StatusInternalServerError)
			return
		}
	}

	if err := s.SettingsManager.UpdateSettings(cameraid, body); err != nil {
		s.Logger.Errorf("Failed to process settings for ESN %s: %v", cameraid, err)
		http.Error(w, "Failed to process settings", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) eventsHandler(w http.ResponseWriter, r *http.Request) {
	// Check for drain=true parameter first (global DB reset for testing)
	isDrain := r.URL.Query().Get("drain") == "true"

	if isDrain {
		// Drain mode - clear entire database for all ESNs (testing)
		s.handleDrainMode(w, r)
		return
	}

	// For non-drain modes, cameraid is required
	cameraid := r.URL.Query().Get("cameraid")
	if cameraid == "" {
		http.Error(w, "cameraid parameter required", http.StatusBadRequest)
		return
	}

	// Check for bridge=true parameter for destructive reads
	isBridge := r.URL.Query().Get("bridge") == "true"
	// Check for list=true parameter for array responses
	isList := r.URL.Query().Get("list") == "true"

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")

	if isBridge {
		// Bridge destructive read - get single ANNT event
		anntStructures, err := s.ANNTStore.GetANNTStructuresForCamera(cameraid, 1)
		if err != nil {
			s.Logger.Errorf("Failed to get ANNT structure (bridge): %v", err)
			http.Error(w, "Failed to retrieve ANNT structure", http.StatusInternalServerError)
			return
		}

		if len(anntStructures) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// Serve single ANNT event (not as array)
		singleEvent := anntStructures[0]
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(singleEvent); err != nil {
			s.Logger.Errorf("Failed to encode ANNT structure as JSON: %v", err)
			http.Error(w, "Failed to serialize ANNT structure", http.StatusInternalServerError)
			return
		}

		s.Logger.Infof("Bridge consumed 1 ANNT structure (DESTRUCTIVE)")
	} else if isList {
		// List mode - return all available events as array (for test scripts)
		anntStructures, err := s.ANNTStore.GetANNTStructuresReadOnly(cameraid, 100) // Get up to 100 events
		if err != nil {
			s.Logger.Errorf("Failed to get ANNT structures (list): %v", err)
			http.Error(w, "Failed to retrieve ANNT structures", http.StatusInternalServerError)
			return
		}

		// Always return array, even if empty
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(anntStructures); err != nil {
			s.Logger.Errorf("Failed to encode ANNT structures as JSON: %v", err)
			http.Error(w, "Failed to serialize ANNT structures", http.StatusInternalServerError)
			return
		}

		s.Logger.Infof("Served %d ANNT structures (LIST)", len(anntStructures))
	} else {
		// Default read-only access - get single ANNT for monitoring
		anntStructures, err := s.ANNTStore.GetANNTStructuresReadOnly(cameraid, 1)
		if err != nil {
			s.Logger.Errorf("Failed to get ANNT structure (read-only): %v", err)
			http.Error(w, "Failed to retrieve ANNT structure", http.StatusInternalServerError)
			return
		}

		if len(anntStructures) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// Serve single ANNT event (not as array)
		singleEvent := anntStructures[0]
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(singleEvent); err != nil {
			s.Logger.Errorf("Failed to encode ANNT structure as JSON: %v", err)
			http.Error(w, "Failed to serialize ANNT structure", http.StatusInternalServerError)
			return
		}

		s.Logger.Infof("Served 1 ANNT structure (READ-ONLY)")
	}
}

func (s *Server) registerDataHandler(w http.ResponseWriter, r *http.Request) {
	incomingDataMutex.RLock()
	data := make([]incomingDataLog, len(incomingData))
	copy(data, incomingData)
	incomingDataMutex.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"total_events":  len(data),
		"recent_events": data,
		"server_status": "active",
		"last_event_at": func() *time.Time {
			if len(data) > 0 {
				return &data[len(data)-1].Timestamp
			}
			return nil
		}(),
	})
}

func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")

	// Get database metrics - just the essentials
	var dbMetrics map[string]interface{}
	if s.ANNTStore != nil {
		if db := s.ANNTStore.GetDB(); db != nil {
			totalKeys, totalSize := db.EstimateSize(nil)

			dbMetrics = map[string]interface{}{
				"total_events": totalKeys,
				"size_mb":      totalSize / 1024 / 1024,
				"status":       "ok",
			}

			// Quick ESN summary - which ESNs have data
			esns := s.getESNsWithData()
			if len(esns) > 0 {
				dbMetrics["active_esns"] = esns
			}
		} else {
			dbMetrics = map[string]interface{}{
				"status": "unavailable",
			}
		}
	} else {
		dbMetrics = map[string]interface{}{
			"status": "not_initialized",
		}
	}

	// Get IP-to-ESN mappings from active vendor
	var routingMetrics map[string]interface{}
	if s.GetActiveVendor != nil {
		if vendor := s.GetActiveVendor(); vendor != nil {
			allMappings := vendor.GetIPESNMappings()

			// Filter out static baseline IPs from metrics if real IPs exist
			staticBaselineIPs := []string{"172.31.99.1", "172.31.99.2"}
			filteredMappings := make(map[string]string)
			hasRealIPs := false

			// First pass: check if we have any real (non-static) IPs
			for ip := range allMappings {
				isStatic := false
				for _, staticIP := range staticBaselineIPs {
					if ip == staticIP {
						isStatic = true
						break
					}
				}
				if !isStatic {
					hasRealIPs = true
					break
				}
			}

			// Second pass: filter mappings based on whether real IPs exist
			for ip, esn := range allMappings {
				if hasRealIPs {
					// If real IPs exist, only show real IPs (hide static ones)
					isStatic := false
					for _, staticIP := range staticBaselineIPs {
						if ip == staticIP {
							isStatic = true
							break
						}
					}
					if !isStatic {
						filteredMappings[ip] = esn
					}
				} else {
					// If no real IPs, show all (including static)
					filteredMappings[ip] = esn
				}
			}

			routingMetrics = map[string]interface{}{
				"ip_to_esn_mappings": filteredMappings,
				"total_mappings":     len(filteredMappings),
				"static_filtered":    hasRealIPs,
			}
		} else {
			routingMetrics = map[string]interface{}{
				"status": "no_active_vendor",
			}
		}
	} else {
		routingMetrics = map[string]interface{}{
			"status": "not_configured",
		}
	}

	// Get operational metrics
	hostname, _ := os.Hostname()

	response := map[string]interface{}{
		"service": map[string]interface{}{
			"uptime_seconds": time.Since(serviceStartTime).Seconds(),
			"mode":           os.Getenv("MODE"),
			"pid":            os.Getpid(),
			"hostname":       hostname,
		},
		"database":  dbMetrics,
		"routing":   routingMetrics,
		"timestamp": time.Now(),
	}

	_ = json.NewEncoder(w).Encode(response)
	s.Logger.Info("Served metrics")
}

// getESNsWithData quickly scans for ESNs that have pending events
func (s *Server) getESNsWithData() []string {
	var esns []string
	seen := make(map[string]bool)

	if s.ANNTStore == nil {
		return esns
	}

	db := s.ANNTStore.GetDB()
	if db == nil {
		return esns
	}

	_ = db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false // Key-only scan for speed
		it := txn.NewIterator(opts)
		defer it.Close()

		prefix := []byte("pending_")
		for it.Seek(prefix); it.ValidForPrefix(prefix) && len(esns) < 10; it.Next() {
			key := string(it.Item().Key())
			// Extract ESN from key format: "pending_<ESN>_<timestamp>_<id>"
			parts := strings.Split(key, "_")
			if len(parts) >= 2 && !seen[parts[1]] {
				esns = append(esns, parts[1])
				seen[parts[1]] = true
			}
		}
		return nil
	})

	return esns
}

func (s *Server) driverHandler(w http.ResponseWriter, r *http.Request) {
	luaScript, err := os.ReadFile("internal/bridge/point_of_sale.lua")
	if err != nil {
		s.Logger.Errorf("Failed to read point_of_sale.lua: %v", err)
		http.Error(w, "Script not found", http.StatusInternalServerError)
		return
	}

	// Bridge-specific checksum functionality for driver file caching
	isBridge := r.URL.Query().Get("bridge") == "true"

	if isBridge {
		// Bridge clients get checksum optimization for caching
		hash := fmt.Sprintf("%x", md5.Sum(luaScript))

		// Check if client has current version
		if clientHash := r.Header.Get("x-een-content-md5"); clientHash == hash {
			s.Logger.Infof("Bridge driver cache hit (MD5: %s)", hash[:8])
			w.WriteHeader(http.StatusNotModified)
			return
		}

		// Set hash header for bridge client caching
		w.Header().Set("x-een-content-md5", hash)
		w.Header().Set("Content-Type", "application/x-lua")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(luaScript)))
		s.Logger.Infof("Serving driver to bridge with MD5: %s", hash[:8])
	} else {
		// Non-bridge clients get driver without caching optimization
		w.Header().Set("Content-Type", "application/x-lua")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(luaScript)))
		s.Logger.Infof("Serving driver (no caching)")
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(luaScript)
}

// handleDrainMode handles drain=true parameter for global database reset (testing)
func (s *Server) handleDrainMode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")

	// Drain all ANNT events across ALL cameras
	anntStructures, err := s.ANNTStore.DrainAllANNTs()
	if err != nil {
		s.Logger.Errorf("Failed to drain all ANNT structures: %v", err)
		http.Error(w, "Failed to drain ANNT structures", http.StatusInternalServerError)
		return
	}

	// Always return array, even if empty
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(anntStructures); err != nil {
		s.Logger.Errorf("Failed to encode drained ANNT structures as JSON: %v", err)
		http.Error(w, "Failed to serialize drained ANNT structures", http.StatusInternalServerError)
		return
	}

	s.Logger.Infof("DRAINED all ANNT structures from database: %d events", len(anntStructures))
}

func (s *Server) configHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")

	response := map[string]interface{}{
		"mode": os.Getenv("MODE"),
	}

	_ = json.NewEncoder(w).Encode(response)
}
