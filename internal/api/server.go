package api

import (
	"bridge-devices-pos/internal/core"
	"bridge-devices-pos/internal/settings"
	"context"
	"net/http"
	"time"

	"github.com/eencloud/goeen/log"
)

// Vendor interface for accessing vendor information
type Vendor interface {
	GetIPESNMappings() map[string]string
}

// VendorGetter function type for getting the active vendor
type VendorGetter func() Vendor

// Server handles HTTP communication from bridge-core.
type Server struct {
	*http.Server
	Logger          *log.Logger
	SettingsManager *settings.Manager
	ANNTStore       *core.ANNTStore // ANNT architecture
	GetActiveVendor VendorGetter
}

// NewServer creates and configures a new server for bridge-core communication.
func NewServer(addr string, logger *log.Logger, sm *settings.Manager, anntStore *core.ANNTStore, vendorGetter VendorGetter) *Server {
	mux := http.NewServeMux()

	s := &Server{
		Server: &http.Server{
			Addr:           addr,
			Handler:        mux,
			ReadTimeout:    5 * time.Second,
			WriteTimeout:   10 * time.Second,
			MaxHeaderBytes: 1 << 20,
		},
		Logger:          logger,
		SettingsManager: sm,
		ANNTStore:       anntStore,
		GetActiveVendor: vendorGetter,
	}

	mux.HandleFunc("/pos_config", s.settingsHandler)        // Bridge-core sends config updates here
	mux.HandleFunc("/events", s.eventsHandler)              // Bridge-core polls for events
	mux.HandleFunc("/register_data", s.registerDataHandler) // Debug endpoint for raw JSON data
	mux.HandleFunc("/config", s.configHandler)              // Debug endpoint for resolved config
	mux.HandleFunc("/metrics", s.metricsHandler)            // Bridge-core heartbeat for future application metric etags
	mux.HandleFunc("/driver", s.driverHandler)              // Serve the Lua script for discovery

	return s
}

// Start begins listening for HTTP requests.
func (s *Server) Start() error {
	s.Logger.Infof("Starting API Server on %s", s.Addr)
	return s.ListenAndServe()
}

// Stop gracefully shuts down the server.
func (s *Server) Stop(ctx context.Context) error {
	s.Logger.Info("Shutting down API Server...")
	return s.Shutdown(ctx)
}
