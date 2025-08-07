package seveneleven

import (
	"bridge-devices-pos/internal/api"
	"bridge-devices-pos/internal/core"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	goeen_log "github.com/eencloud/goeen/log"
)

const (
	// How often to scan for and clean up idle handlers.
	handlerCleanupInterval = 5 * time.Minute
	handlerIdleTimeout     = 15 * time.Minute
)

// --- Structs ---

type RegisterConfig struct {
	StoreNumber    string `json:"store_number"`
	TerminalNumber string `json:"terminal_number"`
	IPAddress      string `json:"ip_address"`
}

type Ingestor struct {
	*http.Server
	logger        *goeen_log.Logger
	processor     *Processor
	handlers      sync.Map
	settings      Settings
	etagQueue     chan<- []byte
	ipToESNs      map[string][]string
	ipToStoreInfo map[string]RegisterConfig
	stateMachine  *IPBasedStateMachine
	settingsMutex sync.RWMutex
	auditLogger   *core.AuditLogger
}

type RegisterHandler struct {
	ip           string
	queue        chan []byte
	stopChan     chan struct{}
	wg           sync.WaitGroup
	logger       *goeen_log.Logger
	processor    *Processor
	state        *TransactionState
	etagQueue    chan<- []byte
	lastActivity time.Time
	mu           sync.Mutex
	targetESNs   []string
	stateMachine *IPBasedStateMachine
}

// --- Ingestor Methods ---

func NewIngestor(logger *goeen_log.Logger, settings Settings, processor *Processor, etagQueue chan<- []byte, stateMachine *IPBasedStateMachine) *Ingestor {
	addr := fmt.Sprintf(":%d", settings.ListenPort)

	// Create standard logger for AuditLogger and determine data directory
	stdLogger := log.New(log.Writer(), "", log.LstdFlags)
	dataDir := core.GetDataDirectory()
	auditLogDir := filepath.Join(dataDir, "audit_logs")
	auditLogger := core.NewAuditLogger(auditLogDir, 100, stdLogger)

	ingestor := &Ingestor{
		logger:        logger,
		processor:     processor,
		handlers:      sync.Map{},
		settings:      settings,
		etagQueue:     etagQueue,
		ipToESNs:      make(map[string][]string),
		ipToStoreInfo: make(map[string]RegisterConfig),
		stateMachine:  stateMachine,
		auditLogger:   auditLogger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", ingestor.rootHandler)
	ingestor.Server = &http.Server{Addr: addr, Handler: mux}
	return ingestor
}

func (i *Ingestor) Start() error {
	i.logger.Infof("7-Eleven Ingestor listening on %s", i.Addr)
	go i.cleanupLoop()
	err := i.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (i *Ingestor) Stop(ctx context.Context) error {
	i.logger.Infof("Shutting down 7-Eleven Ingestor and all register handlers...")
	i.handlers.Range(func(key, value interface{}) bool {
		value.(*RegisterHandler).Stop()
		return true
	})
	return i.Shutdown(ctx)
}

func (i *Ingestor) getEffectiveIP(r *http.Request) string {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = strings.Split(r.RemoteAddr, ":")[0]
	}

	if ip == "127.0.0.1" || ip == "::1" {
		if urlIP := strings.TrimPrefix(r.URL.Path, "/"); urlIP != "" && urlIP != "/" {
			return urlIP
		}
	}

	return ip
}

func (i *Ingestor) rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST method is allowed", http.StatusMethodNotAllowed)
		return
	}
	ip := i.getEffectiveIP(r)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Error reading request body", http.StatusInternalServerError)
		return
	}

	// Log to audit log
	if err := i.auditLogger.Log(ip, body); err != nil {
		i.logger.Errorf("Failed to write audit log: %v", err)
	}

	i.getOrCreateHandler(ip).Enqueue(body)
	w.WriteHeader(http.StatusOK)
}

func (i *Ingestor) getOrCreateHandler(ip string) *RegisterHandler {
	if handler, ok := i.handlers.Load(ip); ok {
		return handler.(*RegisterHandler)
	}

	esns := i.findESNsForIP(ip)
	primaryESN := esns[0]
	newHandler := NewRegisterHandler(ip, i.logger, i.processor, i.etagQueue, primaryESN, esns, i.stateMachine)
	actual, loaded := i.handlers.LoadOrStore(ip, newHandler)
	if loaded {
		newHandler.Stop()
		return actual.(*RegisterHandler)
	}
	go newHandler.Process()
	return newHandler
}

func (i *Ingestor) findESNsForIP(ip string) []string {
	// Use state machine's IP-to-ESN mapping for consistency
	if i.stateMachine != nil {
		esn := i.stateMachine.getESNForIP(ip)
		if esn != "" && esn != InvalidESN {
			i.logger.Infof("Found ESN for IP %s: %s", ip, esn)
			return []string{esn}
		}
	}

	i.logger.Warningf("No ESN found for IP %s, using current ESN as fallback", ip)
	return []string{i.getCurrentESN()}
}

func (i *Ingestor) getCurrentESN() string {
	i.settingsMutex.RLock()
	defer i.settingsMutex.RUnlock()

	// If we have any IP-ESN mappings, return the first ESN found
	for _, esns := range i.ipToESNs {
		if len(esns) > 0 {
			return esns[0]
		}
	}

	// If no mappings exist, try to parse from registers
	var registerConfigs []RegisterConfig
	if err := json.Unmarshal(i.settings.Registers, &registerConfigs); err != nil {
		return InvalidESN
	}

	// No registers configured, return invalid ESN
	if len(registerConfigs) == 0 {
		return InvalidESN
	}

	// Return a default ESN since we don't have specific mappings
	return DefaultESN
}

func (i *Ingestor) AddESNRegisters(esn string, registers []RegisterConfig) {
	i.settingsMutex.Lock()
	defer i.settingsMutex.Unlock()

	for _, reg := range registers {
		ip := reg.IPAddress
		if _, exists := i.ipToESNs[ip]; !exists {
			i.ipToESNs[ip] = []string{}
		}

		// Check if ESN already exists for this IP
		found := false
		for _, existingESN := range i.ipToESNs[ip] {
			if existingESN == esn {
				found = true
				break
			}
		}

		// Add ESN if not already present
		if !found {
			i.ipToESNs[ip] = append(i.ipToESNs[ip], esn)
			i.logger.Infof("Added ESN %s for IP %s", esn, ip)
		}
	}

	i.logger.Infof("Updated mappings for ESN %s with %d registers", esn, len(registers))
}

func (i *Ingestor) cleanupLoop() {
	ticker := time.NewTicker(handlerCleanupInterval)
	defer ticker.Stop()
	for range ticker.C {
		i.handlers.Range(func(key, value interface{}) bool {
			handler := value.(*RegisterHandler)
			if handler.IsIdle() {
				i.logger.Infof("Removing idle handler for IP: %s", handler.ip)
				handler.Stop()
				i.handlers.Delete(key)
			}
			return true
		})
	}
}

// --- RegisterHandler Methods ---

func NewRegisterHandler(ip string, logger *goeen_log.Logger, processor *Processor, etagQueue chan<- []byte, primaryESN string, allESNs []string, stateMachine *IPBasedStateMachine) *RegisterHandler {
	return &RegisterHandler{
		ip:           ip,
		queue:        make(chan []byte, 100),
		stopChan:     make(chan struct{}),
		logger:       logger,
		processor:    processor,
		state:        &TransactionState{ESN: primaryESN},
		etagQueue:    etagQueue,
		targetESNs:   allESNs,
		stateMachine: stateMachine,
	}
}

func (h *RegisterHandler) Enqueue(data []byte) {
	h.mu.Lock()
	h.lastActivity = time.Now()
	h.mu.Unlock()
	select {
	case h.queue <- data:
	default:
		h.logger.Warningf("Queue full for register %s. Dropping data.", h.ip)
	}
}

func (h *RegisterHandler) Process() {
	h.wg.Add(1)
	defer h.wg.Done()
	h.logger.Infof("Starting processor for register %s", h.ip)
	idleTimer := time.NewTimer(handlerIdleTimeout)
	for {
		select {
		case data := <-h.queue:
			idleTimer.Reset(handlerIdleTimeout)
			h.processSingleMessage(data)
		case <-idleTimer.C:
			h.logger.Infof("Register handler for %s timed out. Shutting down.", h.ip)
			return
		case <-h.stopChan:
			h.logger.Infof("Stopping processor for register %s", h.ip)
			return
		}
	}
}

func (h *RegisterHandler) processSingleMessage(data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()

	var payload map[string]interface{}
	if err := json.Unmarshal(data, &payload); err != nil {
		h.logger.Errorf("Unmarshal failed for %s: %v", h.ip, err)
		return
	}

	logRawJSONFromIngestor(h.ip, payload)

	if err := h.stateMachine.ProcessEvent(h.ip, data); err != nil {
		h.logger.Errorf("State machine processing failed for %s: %v", h.ip, err)
		return
	}

	h.logger.Debugf("Event processed by state machine: %s", h.ip)
}

func logRawJSONFromIngestor(remoteAddr string, data interface{}) {
	api.LogIncomingData(remoteAddr, data)
}

func (h *RegisterHandler) Stop() {
	close(h.stopChan)
	h.wg.Wait()
}

func (h *RegisterHandler) IsIdle() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return time.Since(h.lastActivity) > handlerIdleTimeout
}
