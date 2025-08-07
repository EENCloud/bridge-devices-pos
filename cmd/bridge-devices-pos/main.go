package main

import (
	"bridge-devices-pos/internal/api"
	"bridge-devices-pos/internal/core"
	"bridge-devices-pos/internal/settings"
	"bridge-devices-pos/internal/vendors"
	_ "bridge-devices-pos/internal/vendors/seveneleven"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/eencloud/goeen/log"
)

func main() {
	// Create a custom context with clean format
	customFormat := "{{eenTimeStamp .Now}}[{{.Level}}]: {{.Message}}"
	customContext := log.NewContext(os.Stderr, customFormat, log.LevelInfo)
	logger := customContext.GetLogger("app", log.LevelInfo)
	// Use Triggerf to provide context instead of NULL
	logger.Triggerf(log.LevelInfo, "startup", "Starting Bridge POS Service...")

	// Determine the best data directory
	dataDir := core.GetDataDirectory()
	logger.Triggerf(log.LevelInfo, "startup", "Using data directory: %s", dataDir)

	anntDbPath := filepath.Join(dataDir, "annt_db")
	anntStore, err := core.NewANNTStore(anntDbPath, 10, logger)
	if err != nil {
		logger.Fatalf("Failed to create ANNT store: %v", err)
	}
	defer func() {
		if err := anntStore.Close(); err != nil {
			logger.Errorf("Failed to close ANNT store: %v", err)
		}
	}()

	settingsManager := settings.NewManager(logger)

	var activeVendor vendors.Vendor

	settingsManager.SetUpdateCallback(func(esn string, vendor *settings.VendorConfig) {
		if activeVendor != nil {
			activeVendor.HandleESNConfiguration(esn, vendor)
		}
	})

	// Create vendor getter function for API server
	getActiveVendor := func() api.Vendor {
		if activeVendor != nil {
			// Type assertion to api.Vendor interface
			if apiVendor, ok := activeVendor.(api.Vendor); ok {
				return apiVendor
			}
		}
		return nil
	}

	apiServer := api.NewServer(":33480", logger, settingsManager, anntStore, getActiveVendor)

	go func() {
		if err := apiServer.Start(); err != nil {
			logger.Fatalf("API Server failed: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mode := os.Getenv("MODE")
	if mode == "simulation" {
		logger.Info("Simulation mode detected. Looking for vendor test configs...")
		if err := loadSimulatorConfig(logger, settingsManager); err != nil {
			logger.Errorf("Failed to load simulator config: %v", err)
		}
	}

	logger.Triggerf(log.LevelInfo, "startup", "Service started. Waiting for settings from Bridge...")

	for {
		select {
		case <-settingsManager.Changes():
			logger.Triggerf(log.LevelInfo, "config-change", "Settings change detected. Checking if vendor restart needed...")

			vendorConfig := settingsManager.GetActiveVendor()

			shouldRestart := false
			if activeVendor == nil {
				logger.Info("No active vendor - starting new vendor")
				shouldRestart = true
			} else if vendorConfig == nil {
				logger.Info("No vendor configuration - stopping current vendor")
				shouldRestart = true
			} else if activeVendor.Name() != vendorConfig.Brand {
				logger.Infof("Vendor brand changed from %s to %s - restarting", activeVendor.Name(), vendorConfig.Brand)
				shouldRestart = true
			} else {
				logger.Infof("Vendor %s already active - no restart needed, just updating configuration", vendorConfig.Brand)
			}

			if shouldRestart {
				if activeVendor != nil {
					logger.Infof("Stopping current vendor: %s", activeVendor.Name())
					if err := activeVendor.Stop(ctx); err != nil {
						logger.Errorf("Error stopping vendor %s: %v", activeVendor.Name(), err)
					}
					activeVendor = nil
				}

				if vendorConfig != nil {
					newFunc, err := vendors.Get(vendorConfig.Brand)
					if err != nil {
						logger.Errorf("Failed to get new vendor '%s': %v", vendorConfig.Brand, err)
						continue
					}

					vendorJSON, _ := json.Marshal(vendorConfig)
					newVendor, err := newFunc(logger, vendorJSON, anntStore)
					if err != nil {
						logger.Errorf("Failed to create new vendor '%s': %v", vendorConfig.Brand, err)
						continue
					}

					if err := newVendor.Start(); err != nil {
						logger.Errorf("Failed to start new vendor '%s': %v", vendorConfig.Brand, err)
					} else {
						activeVendor = newVendor

						// Replay existing ESN configurations to the new vendor
						// This fixes a timing issue where configs arrive before vendor starts
						existingConfigs := settingsManager.GetAllESNConfigurations()
						for esn, config := range existingConfigs {
							logger.Infof("Replaying configuration for ESN %s to new vendor", esn)
							activeVendor.HandleESNConfiguration(esn, config)
						}
					}
				} else {
					logger.Info("No active vendor configured.")
				}
			}

		case <-ctx.Done():
			logger.Info("Shutdown signal received...")

			if err := apiServer.Stop(context.Background()); err != nil {
				logger.Errorf("Error stopping API server: %v", err)
			}

			if activeVendor != nil {
				if err := activeVendor.Stop(context.Background()); err != nil {
					logger.Errorf("Error stopping vendor %s: %v", activeVendor.Name(), err)
				}
			}
			logger.Info("Service stopped gracefully.")
			return
		}
	}
}

func loadSimulatorConfig(logger *log.Logger, settingsManager *settings.Manager) error {
	configPath := "data/7eleven/sim-vendor-config.json"

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil
	}

	configData, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read sim config %s: %w", configPath, err)
	}

	var simConfig map[string]interface{}
	if err := json.Unmarshal(configData, &simConfig); err != nil {
		return fmt.Errorf("failed to parse sim config %s: %w", configPath, err)
	}

	// Extract ESN mappings from sim config
	esnMappings, ok := simConfig["esn_mappings"].(map[string]interface{})
	if !ok || len(esnMappings) == 0 {
		logger.Errorf("No esn_mappings found in sim config %s", configPath)
		return nil
	}

	// Create separate configurations for each ESN
	for ipAddr, esnInterface := range esnMappings {
		esn, ok := esnInterface.(string)
		if !ok {
			logger.Errorf("Invalid ESN mapping for IP %s", ipAddr)
			continue
		}

		// Filter registers to only include the ones for this IP/ESN
		allRegisters, ok := simConfig["registers"].([]interface{})
		if !ok {
			logger.Errorf("Invalid registers format in sim config")
			continue
		}

		var filteredRegisters []interface{}
		for _, regInterface := range allRegisters {
			reg, ok := regInterface.(map[string]interface{})
			if !ok {
				continue
			}
			if regIP, exists := reg["ip_address"]; exists && regIP == ipAddr {
				filteredRegisters = append(filteredRegisters, reg)
				break // Only one register per IP
			}
		}

		if len(filteredRegisters) == 0 {
			logger.Errorf("No register found for IP %s in sim config", ipAddr)
			continue
		}

		// Create a vendor config for this specific ESN with only its IP
		vendorConfig := map[string]interface{}{
			"brand":       simConfig["brand"],
			"listen_port": 6324, // Force simulation port
			"registers":   filteredRegisters,
		}

		simulatorPayload := map[string]interface{}{
			"esn":     esn,
			"vendors": []interface{}{vendorConfig},
		}

		payloadBytes, _ := json.Marshal(simulatorPayload)
		if err := settingsManager.UpdateSettings(esn, payloadBytes); err != nil {
			logger.Errorf("Failed to load simulator config for ESN %s: %v", esn, err)
			continue
		}

		logger.Infof("Loaded simulator config for ESN %s (IP: %s)", esn, ipAddr)
	}

	return nil
}
