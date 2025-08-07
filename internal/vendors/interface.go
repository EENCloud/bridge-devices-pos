package vendors

import (
	"bridge-devices-pos/internal/core"
	"bridge-devices-pos/internal/settings"
	"context"
	"encoding/json"

	"github.com/eencloud/goeen/log"
)

// Vendor represents a POS vendor that can process transactions.
type Vendor interface {
	Name() string
	Start() error
	Stop(ctx context.Context) error
	HandleESNConfiguration(esn string, vendor *settings.VendorConfig)
	GetIPESNMappings() map[string]string
}

// NewFunc is a function signature for creating a new vendor instance.
// It will be passed the vendor-specific section of the settings and the ANNT store.
type NewFunc func(logger *log.Logger, vendorConfig json.RawMessage, anntStore *core.ANNTStore) (Vendor, error)
