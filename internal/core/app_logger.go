package core

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARN
	ERROR
)

type AppLogger struct {
	logger      *log.Logger
	level       LogLevel
	logFile     *os.File
	mutex       sync.Mutex
	component   string
	environment string
	version     string
}

type LogEntry struct {
	Timestamp string                 `json:"timestamp"`
	LogLevel  string                 `json:"level"`
	Function  string                 `json:"function"`
	File      string                 `json:"file"`
	LineNo    int                    `json:"line"`
	Message   string                 `json:"message"`
	Extra     map[string]interface{} `json:"extra,omitempty"`
	Error     string                 `json:"error,omitempty"`
}

func NewAppLogger(component, logFilePath string, level LogLevel) (*AppLogger, error) {
	return NewAppLoggerWithContext(component, logFilePath, "development", "1.0.0", level)
}

func NewAppLoggerWithContext(component, logFilePath, environment, version string, level LogLevel) (*AppLogger, error) {
	var logFile *os.File
	var err error

	if logFilePath != "" {
		logFile, err = os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, fmt.Errorf("failed to open log file: %w", err)
		}
	}

	logger := log.New(os.Stdout, "", 0)
	if logFile != nil {
		logger.SetOutput(logFile)
	}

	return &AppLogger{
		logger:      logger,
		level:       level,
		logFile:     logFile,
		component:   component,
		environment: environment,
		version:     version,
	}, nil
}

func (a *AppLogger) log(level LogLevel, message string, data map[string]interface{}, err error) {
	if level < a.level {
		return
	}

	a.mutex.Lock()
	defer a.mutex.Unlock()

	// Get caller info
	_, file, line, ok := runtime.Caller(2)
	function := "unknown"
	if ok {
		if pc, _, _, ok := runtime.Caller(2); ok {
			if fn := runtime.FuncForPC(pc); fn != nil {
				function = fn.Name()
			}
		}
	}

	// Clean up the file path - just show filename instead of full path
	if file != "" {
		if idx := strings.LastIndex(file, "/"); idx != -1 {
			file = file[idx+1:]
		}
	}

	// Skip data if it's empty to avoid NULL in logs
	var extraData map[string]interface{}
	if len(data) > 0 {
		extraData = data
	}

	entry := LogEntry{
		Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		LogLevel:  a.levelToString(level),
		Function:  function,
		File:      file,
		LineNo:    line,
		Message:   message,
		Extra:     extraData,
	}

	if err != nil {
		entry.Error = err.Error()
	}

	jsonBytes, marshalErr := json.Marshal(entry)
	if marshalErr != nil {
		a.logger.Printf("LOG_ERROR: Failed to marshal log entry: %v", marshalErr)
		return
	}

	a.logger.Println(string(jsonBytes))
}

func (a *AppLogger) levelToString(level LogLevel) string {
	switch level {
	case DEBUG:
		return "DEBUG"
	case INFO:
		return "INFO"
	case WARN:
		return "WARNING"
	case ERROR:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

func (a *AppLogger) Debug(message string, data map[string]interface{}) {
	a.log(DEBUG, message, data, nil)
}

func (a *AppLogger) Info(message string, data map[string]interface{}) {
	a.log(INFO, message, data, nil)
}

func (a *AppLogger) Warn(message string, data map[string]interface{}) {
	a.log(WARN, message, data, nil)
}

func (a *AppLogger) Error(message string, data map[string]interface{}, err error) {
	a.log(ERROR, message, data, err)
}

// ===== ESSENTIAL OPERATIONAL LOGGING (INFO LEVEL) =====

// 1. CORE OPERATIONS - Always visible
func (a *AppLogger) LogStartup(config map[string]interface{}) {
	vendor := "unknown"
	simulatorMode := false
	if v, ok := config["vendor"].(string); ok {
		vendor = v
	}
	if s, ok := config["simulator_mode"].(bool); ok {
		simulatorMode = s
	}

	a.Info("bridge-devices-pos started", map[string]interface{}{
		"environment":    a.environment,
		"version":        a.version,
		"vendor":         vendor,
		"simulator_mode": simulatorMode,
		"real_data":      !simulatorMode,
		"startup_time":   time.Now().UTC().Format(time.RFC3339),
	})
}

func (a *AppLogger) LogBridgeConfigReceived(source string, config map[string]interface{}) {
	a.Info("bridge configuration received", map[string]interface{}{
		"source":      source,
		"config_keys": getMapKeys(config),
		"success":     true,
	})
}

func (a *AppLogger) LogBridgeConfigFailed(source string, err error) {
	a.Error("bridge configuration failed", map[string]interface{}{
		"source": source,
	}, err)
}

// 2. ANNT DELIVERY - MOST IMPORTANT (Human Readable)
func (a *AppLogger) LogANNTFetched(anntID, esn string, deliveryTime time.Duration, success bool) {
	if success {
		a.Info("annt delivered successfully", map[string]interface{}{
			"annt_id":      anntID,
			"esn":          esn,
			"delivery_ms":  deliveryTime.Milliseconds(),
			"delivered_at": time.Now().UTC().Format(time.RFC3339),
		})
	} else {
		a.Error("annt delivery failed", map[string]interface{}{
			"annt_id":     anntID,
			"esn":         esn,
			"delivery_ms": deliveryTime.Milliseconds(),
		}, nil)
	}
}

// 3. RECOVERY OPERATIONS - Important but less frequent
func (a *AppLogger) LogRecoveryStarted(pendingCount int, estimatedTime time.Duration) {
	a.Info("recovery started", map[string]interface{}{
		"pending_annts":    pendingCount,
		"estimated_min":    estimatedTime.Minutes(),
		"recovery_started": time.Now().UTC().Format(time.RFC3339),
	})
}

func (a *AppLogger) LogRecoveryCompleted(totalProcessed int, duration time.Duration) {
	a.Info("recovery completed", map[string]interface{}{
		"total_processed": totalProcessed,
		"duration_min":    duration.Minutes(),
		"completed_at":    time.Now().UTC().Format(time.RFC3339),
	})
}

// 4. CRITICAL ISSUES - Always visible
func (a *AppLogger) LogCriticalError(operation string, err error, context map[string]interface{}) {
	logData := map[string]interface{}{
		"operation": operation,
		"critical":  true,
	}

	for k, v := range context {
		logData[k] = v
	}

	a.Error("critical error", logData, err)
}

func (a *AppLogger) LogDataLoss(operation string, lostCount int, details map[string]interface{}) {
	logData := map[string]interface{}{
		"operation":  operation,
		"lost_count": lostCount,
		"data_loss":  true,
	}

	for k, v := range details {
		logData[k] = v
	}

	a.Error("data loss detected", logData, nil)
}

// ===== DETAILED TRACKING (DEBUG LEVEL) =====

// ETag creation details - useful for debugging and simulation
func (a *AppLogger) LogETagCreated(etagData []byte) {
	humanReadable := a.makeETagHumanReadable(etagData)

	a.Debug("etag created", map[string]interface{}{
		"etag": humanReadable,
	})
}

// Bridge delivery confirmation - always important for production
func (a *AppLogger) LogBridgeResponse(etagID string, statusCode int, responseTime time.Duration, success bool) {
	if success {
		a.Info("etag accepted by bridge", map[string]interface{}{
			"etag_id":      etagID,
			"status_code":  statusCode,
			"response_ms":  responseTime.Milliseconds(),
			"delivered_at": time.Now().UTC().Format(time.RFC3339),
		})
	} else {
		a.Error("etag rejected by bridge", map[string]interface{}{
			"etag_id":     etagID,
			"status_code": statusCode,
			"response_ms": responseTime.Milliseconds(),
		}, nil)
	}
}

// Simulation mode logging - more verbose for development
func (a *AppLogger) LogETagCreatedSimulation(etagData []byte, simulationMode bool) {
	humanReadable := a.makeETagHumanReadable(etagData)

	if simulationMode {
		// In simulation mode, promote to INFO for visibility
		a.Info("etag created (simulation)", map[string]interface{}{
			"etag":            humanReadable,
			"simulation_mode": true,
		})
	} else {
		// Production mode - keep as DEBUG
		a.Debug("etag created", map[string]interface{}{
			"etag": humanReadable,
		})
	}
}

// Simulated bridge response for development
func (a *AppLogger) LogSimulatedBridgeResponse(etagID string, responseTime time.Duration) {
	a.Info("etag accepted by simulated bridge", map[string]interface{}{
		"etag_id":      etagID,
		"status_code":  200,
		"response_ms":  responseTime.Milliseconds(),
		"simulated":    true,
		"delivered_at": time.Now().UTC().Format(time.RFC3339),
	})
}

// State machine internals - debug only
func (a *AppLogger) LogStateTransition(registerIP, transactionSeq, fromState, toState string) {
	a.Debug("state transition", map[string]interface{}{
		"register_ip":     registerIP,
		"transaction_seq": transactionSeq,
		"from_state":      fromState,
		"to_state":        toState,
	})
}

func (a *AppLogger) LogStateTransitionWithEvent(registerIP, transactionSeq, fromState, toState, eventType string) {
	a.Debug("state transition", map[string]interface{}{
		"register_ip":     registerIP,
		"transaction_seq": transactionSeq,
		"from_state":      fromState,
		"to_state":        toState,
		"trigger_event":   eventType,
	})
}

func (a *AppLogger) LogTransactionComplete(registerIP, transactionSeq string, eventCount int, duration time.Duration) {
	a.Debug("transaction complete", map[string]interface{}{
		"register_ip":     registerIP,
		"transaction_seq": transactionSeq,
		"event_count":     eventCount,
		"duration_ms":     duration.Milliseconds(),
	})
}

func (a *AppLogger) LogUnknownEventProcessed(registerIP, eventType string, groupSize int) {
	a.Debug("unknown event processed", map[string]interface{}{
		"register_ip": registerIP,
		"event_type":  eventType,
		"group_size":  groupSize,
	})
}

func (a *AppLogger) LogRecoveryProgress(processed, remaining int, rate float64) {
	percentComplete := float64(0)
	if processed+remaining > 0 {
		percentComplete = float64(processed) / float64(processed+remaining) * 100
	}

	a.Debug("recovery progress", map[string]interface{}{
		"processed":        processed,
		"remaining":        remaining,
		"percent_complete": percentComplete,
		"rate":             rate,
	})
}

// ===== LEGACY METHODS (Keep for backward compatibility) =====

func (a *AppLogger) LogVendorMode(vendor string, simulatorMode bool) {
	a.Debug("vendor mode set", map[string]interface{}{
		"vendor":         vendor,
		"simulator_mode": simulatorMode,
	})
}

func (a *AppLogger) LogANNTDelivered(anntID, esn string, deliveryTime time.Duration) {
	// Redirect to the more important LogANNTFetched
	a.LogANNTFetched(anntID, esn, deliveryTime, true)
}

// Remove bloat - these are now no-ops or moved to debug
func (a *AppLogger) LogANNTDeliveryMetrics(totalDelivered, totalFailed int64, avgDeliveryTime time.Duration) {
	// This is monitoring data, not operational logging
	a.Debug("delivery metrics", map[string]interface{}{
		"delivered": totalDelivered,
		"failed":    totalFailed,
		"avg_ms":    avgDeliveryTime.Milliseconds(),
	})
}

func (a *AppLogger) LogRecoveryEvent(eventType string, count int, data map[string]interface{}) {
	a.Debug("recovery event", map[string]interface{}{
		"event_type": eventType,
		"count":      count,
	})
}

func (a *AppLogger) LogPerformanceMetrics(component string, metrics map[string]interface{}) {
	// Performance metrics should go to monitoring system, not app logs
	a.Debug("performance", map[string]interface{}{
		"component": component,
	})
}

func (a *AppLogger) LogHealthCheck(component string, healthy bool, details map[string]interface{}) {
	// Health checks are too frequent for normal logs
	a.Debug("health check", map[string]interface{}{
		"component": component,
		"healthy":   healthy,
	})
}

func (a *AppLogger) LogConfigUpdate(configType string, oldValue, newValue interface{}) {
	a.Debug("config updated", map[string]interface{}{
		"config_type": configType,
		"old_value":   oldValue,
		"new_value":   newValue,
	})
}

func (a *AppLogger) LogRateLimitTriggered(component string, currentRate, maxRate float64) {
	a.Debug("rate limited", map[string]interface{}{
		"component":    component,
		"current_rate": currentRate,
		"max_rate":     maxRate,
	})
}

// Helper functions
func (a *AppLogger) makeETagHumanReadable(etagData []byte) string {
	if len(etagData) < 48 {
		return fmt.Sprintf("invalid_etag_size_%d", len(etagData))
	}

	header := etagData[:48]
	payloadSize := len(etagData) - 48

	// Decode meaningful header fields
	etagType := string(header[0:4])
	cameraID := binary.LittleEndian.Uint32(header[16:20])
	namespace := binary.LittleEndian.Uint32(header[20:24])
	flags := binary.LittleEndian.Uint32(header[24:28])

	// Extract and format UUID
	var embeddedUUID uuid.UUID
	copy(embeddedUUID[:], header[28:44])

	sequence := binary.LittleEndian.Uint16(header[44:46])

	return fmt.Sprintf("type=%s uuid=%s esn=%08x ns=%d seq=%d payload=%db flags=%x",
		etagType,
		embeddedUUID.String()[:8], // First 8 chars of UUID for brevity
		cameraID,
		namespace,
		sequence,
		payloadSize,
		flags)
}

func getMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func (a *AppLogger) Close() error {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	if a.logFile != nil {
		return a.logFile.Close()
	}
	return nil
}
