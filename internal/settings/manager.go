package settings

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/eencloud/goeen/log"
)

// VendorConfig defines the structure for a single vendor's configuration block.
type VendorConfig struct {
	Brand      string          `json:"brand"`
	ListenPort int             `json:"listen_port"`
	Registers  json.RawMessage `json:"registers"`
}

// FullPayload defines the structure of the entire JSON posted from bridge-core.
type FullPayload struct {
	Vendors []VendorConfig `json:"vendors"`
}

// ESNVendorConfig stores configuration for a specific ESN
type ESNVendorConfig struct {
	ESN    string        `json:"esn"`
	Vendor *VendorConfig `json:"vendor"`
}

// Manager handles the storage and retrieval of POS configurations.
type Manager struct {
	sync.RWMutex
	logger         *log.Logger
	activeVendor   *VendorConfig
	changeChan     chan struct{}
	esnConfigs     map[string]*ESNVendorConfig // ESN -> config
	updateCallback func(esn string, vendor *VendorConfig)
}

// NewManager creates a new configuration manager.
func NewManager(logger *log.Logger) *Manager {
	return &Manager{
		logger:     logger,
		changeChan: make(chan struct{}, 1),
		esnConfigs: make(map[string]*ESNVendorConfig),
	}
}

// UpdateSettings parses the payload and updates the active vendor config.
func (m *Manager) UpdateSettings(esn string, payload []byte) error {
	m.Lock()
	defer m.Unlock()

	var settings FullPayload
	if err := json.Unmarshal(payload, &settings); err != nil {
		return fmt.Errorf("could not unmarshal payload: %w", err)
	}

	// Handle ESN-specific configuration accumulation
	// ESN now comes from parameter instead of JSON payload

	if len(settings.Vendors) > 1 {
		m.logger.Errorf("ESN %s sent settings with %d vendors, but only one is allowed. Ignoring update.", esn, len(settings.Vendors))
		return fmt.Errorf("multiple vendors not allowed")
	}

	if len(settings.Vendors) == 1 {
		vendor := &settings.Vendors[0]
		m.logger.Infof("Received configuration from ESN %s for vendor: %s", esn, vendor.Brand)

		// Store ESN-specific configuration (preserve original registers field)
		m.esnConfigs[esn] = &ESNVendorConfig{
			ESN:    esn,
			Vendor: vendor,
		}

		// Set/update the active vendor (all ESNs use same vendor type at same site)
		if m.activeVendor == nil {
			m.activeVendor = vendor
			m.logger.Infof("Activating vendor: %s", vendor.Brand)
		}

		// Notify vendor about this ESN's configuration
		if m.updateCallback != nil {
			m.updateCallback(esn, vendor)
		}

	} else {
		m.logger.Infof("ESN %s deactivating vendor configuration", esn)
		delete(m.esnConfigs, esn)

		// If no more ESNs have configurations, deactivate the vendor
		if len(m.esnConfigs) == 0 {
			m.logger.Info("No active ESN configurations. Deactivating vendor.")
			m.activeVendor = nil
		}
	}

	m.notifyChange()

	return nil
}

// GetActiveVendor returns a copy of the current active vendor configuration.
func (m *Manager) GetActiveVendor() *VendorConfig {
	m.RLock()
	defer m.RUnlock()

	if m.activeVendor == nil {
		return nil
	}

	vendorCopy := *m.activeVendor
	return &vendorCopy
}

// Changes returns a channel that signals when settings have been updated.
func (m *Manager) Changes() <-chan struct{} {
	return m.changeChan
}

// SetUpdateCallback sets the function to call when ESN configurations are updated
func (m *Manager) SetUpdateCallback(callback func(esn string, vendor *VendorConfig)) {
	m.Lock()
	defer m.Unlock()
	m.updateCallback = callback
}

// GetAllESNConfigurations returns all current ESN configurations for replay to new vendors
func (m *Manager) GetAllESNConfigurations() map[string]*VendorConfig {
	m.RLock()
	defer m.RUnlock()

	result := make(map[string]*VendorConfig)
	for esn, config := range m.esnConfigs {
		result[esn] = config.Vendor
	}
	return result
}

func (m *Manager) notifyChange() {
	select {
	case m.changeChan <- struct{}{}:
	default:
	}
}
