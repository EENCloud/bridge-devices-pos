package core

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

type AuditLogger struct {
	logDir    string
	maxSizeMB int64
	mutex     sync.Mutex
	logger    *log.Logger
}

func NewAuditLogger(logDir string, maxSizeMB int64, logger *log.Logger) *AuditLogger {
	_ = os.MkdirAll(logDir, 0o755)
	return &AuditLogger{
		logDir:    logDir,
		maxSizeMB: maxSizeMB,
		logger:    logger,
	}
}

func (a *AuditLogger) Log(registerIP string, jsonData []byte) error {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	entry := map[string]interface{}{
		"timestamp":   time.Now().UTC().Format(time.RFC3339Nano),
		"register_ip": registerIP,
		"raw_json":    json.RawMessage(jsonData),
	}

	entryBytes, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal audit entry: %w", err)
	}
	entryBytes = append(entryBytes, '\n')

	filename := a.getCurrentLogFile()
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open audit log: %w", err)
	}
	defer func() { _ = file.Close() }()

	if _, err = file.Write(entryBytes); err != nil {
		return fmt.Errorf("failed to write audit entry: %w", err)
	}

	// Check if rotation is needed
	if err := a.checkRotation(filename); err != nil {
		a.logger.Printf("Audit rotation error: %v", err)
	}

	return nil
}

func (a *AuditLogger) getCurrentLogFile() string {
	return fmt.Sprintf("%s/audit_%s.jsonl", a.logDir, time.Now().Format("20060102_15"))
}

func (a *AuditLogger) checkRotation(filename string) error {
	stat, err := os.Stat(filename)
	if err != nil {
		return err
	}

	sizeMB := stat.Size() / (1024 * 1024)
	if sizeMB >= a.maxSizeMB {
		return a.rotateLog(filename)
	}

	return nil
}

func (a *AuditLogger) rotateLog(filename string) error {
	timestamp := time.Now().Format("20060102_150405")

	rotatedFile := fmt.Sprintf("%s.rotated_%s", filename, timestamp)

	if err := os.Rename(filename, rotatedFile); err != nil {
		return fmt.Errorf("failed to rotate log file: %w", err)
	}

	a.logger.Printf("Rotated audit log: %s -> %s (ready for tar/SCP)", filename, rotatedFile)

	return nil
}

func (a *AuditLogger) GetStats() map[string]interface{} {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	currentFile := a.getCurrentLogFile()
	var currentSize int64
	if stat, err := os.Stat(currentFile); err == nil {
		currentSize = stat.Size()
	}

	return map[string]interface{}{
		"current_file":    currentFile,
		"current_size_mb": currentSize / (1024 * 1024),
		"max_size_mb":     a.maxSizeMB,
		"rotation_needed": currentSize >= (a.maxSizeMB * 1024 * 1024),
	}
}
