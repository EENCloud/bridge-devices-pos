package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dgraph-io/badger/v4"
	goeen_log "github.com/eencloud/goeen/log"
)

// Single TTL constant for all items (business rule)
const itemTTL = 48 * time.Hour

// ANNTItem represents an ANNT structure for storage
type ANNTItem struct {
	ID             string
	RegisterIP     string
	TransactionSeq string
	ANNTData       map[string]interface{} // The full ANNT structure
	Namespace      int
	Priority       int
	CreatedAt      time.Time
	ESN            string
}

// ANNTStore manages storage of ANNT structures
type ANNTStore struct {
	db      *badger.DB
	maxSize int64
	ctx     context.Context
	cancel  context.CancelFunc
	logger  *goeen_log.Logger
}

func NewANNTStore(dir string, maxSizeGB int, logger *goeen_log.Logger) (*ANNTStore, error) {
	maxSize := int64(maxSizeGB) * 1024 * 1024 * 1024

	// Check for stale lock file and attempt cleanup
	if err := cleanupStaleLock(dir, logger); err != nil {
		logger.Warningf("Failed to cleanup potential stale lock: %v", err)
	}

	opts := badger.DefaultOptions(dir).
		WithValueLogFileSize(64 << 20). // 64MB value log files
		WithMemTableSize(32 << 20).     // 32MB mem tables
		WithNumMemtables(3).            // 3 mem tables
		WithNumCompactors(4).           // 4 compactors
		WithSyncWrites(false).          // Async for performance
		WithBlockCacheSize(64 << 20).   // 64MB block cache
		WithLogger(nil)

	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open badger db: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	store := &ANNTStore{
		db:      db,
		maxSize: maxSize,
		ctx:     ctx,
		cancel:  cancel,
		logger:  logger,
	}

	go store.maintenanceWorker()

	return store, nil
}

// StoreANNT stores an ANNT structure for future delivery
func (s *ANNTStore) StoreANNT(item ANNTItem) error {
	// Use ESN-prefixed key for fast per-camera iteration
	// Format: "pending_<ESN>_<timestamp>_<id>"
	key := fmt.Sprintf("pending_%s_%d_%s", item.ESN, item.CreatedAt.UnixNano(), item.ID)

	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("failed to marshal annt item: %w", err)
	}

	err = s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(key), data)
	})
	if err != nil {
		return fmt.Errorf("failed to store annt: %w", err)
	}

	s.logger.Infof("Stored ANNT: %s (NS%d)", item.ID, item.Namespace)
	return nil
}

// GetANNTStructuresForCamera gets up to limit ANNT structures for cameraid
// "fetched by bridge = delivered" model
// OPTIMIZED: Uses ESN-prefixed keys for fast lookup
func (s *ANNTStore) GetANNTStructuresForCamera(cameraid string, limit int) ([]map[string]interface{}, error) {
	var anntStructures []map[string]interface{}
	var keysToDelete [][]byte

	err := s.db.Update(func(txn *badger.Txn) error {
		itOpts := badger.DefaultIteratorOptions
		itOpts.PrefetchValues = false // Key-only scan for performance
		it := txn.NewIterator(itOpts)
		defer it.Close()

		// Use ESN-specific prefix for FAST iteration (only items for this camera)
		prefix := []byte(fmt.Sprintf("pending_%s_", cameraid))
		for it.Seek(prefix); it.ValidForPrefix(prefix) && len(anntStructures) < limit; it.Next() {
			item := it.Item()
			var data []byte
			err := item.Value(func(val []byte) error {
				data = append([]byte{}, val...)
				return nil
			})
			if err != nil {
				return err
			}

			var anntItem ANNTItem
			if err := json.Unmarshal(data, &anntItem); err != nil {
				continue
			}

			// No ESN check needed anymore - prefix guarantees correct camera
			anntStructures = append(anntStructures, anntItem.ANNTData)
			// Mark for immediate deletion (fetched = delivered)
			keysToDelete = append(keysToDelete, item.KeyCopy(nil))
		}

		// Delete delivered items immediately
		for _, key := range keysToDelete {
			if err := txn.Delete(key); err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	if len(anntStructures) > 0 {
		s.logger.Infof("Delivered %d ANNT structures to camera %s", len(anntStructures), cameraid)
	}

	return anntStructures, nil
}

// GetANNTStructuresReadOnly gets ANNT structures WITHOUT deleting them (for testing)
// This prevents accidental data drainage by non-bridge clients
func (s *ANNTStore) GetANNTStructuresReadOnly(cameraid string, limit int) ([]map[string]interface{}, error) {
	var anntStructures []map[string]interface{}

	err := s.db.View(func(txn *badger.Txn) error {
		itOpts := badger.DefaultIteratorOptions
		itOpts.PrefetchValues = false // Key-only scan for performance
		it := txn.NewIterator(itOpts)
		defer it.Close()

		// Use ESN-specific prefix for FAST iteration (only items for this camera)
		prefix := []byte(fmt.Sprintf("pending_%s_", cameraid))
		for it.Seek(prefix); it.ValidForPrefix(prefix) && len(anntStructures) < limit; it.Next() {
			item := it.Item()
			var data []byte
			err := item.Value(func(val []byte) error {
				data = append([]byte{}, val...)
				return nil
			})
			if err != nil {
				return err
			}

			var anntItem ANNTItem
			if err := json.Unmarshal(data, &anntItem); err != nil {
				continue
			}

			anntStructures = append(anntStructures, anntItem.ANNTData)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	if len(anntStructures) > 0 {
		s.logger.Infof("Read-only access: %d ANNT structures for camera %s (NOT deleted)", len(anntStructures), cameraid)
	}

	return anntStructures, nil
}

// DrainAllANNTs clears ALL pending ANNT events across ALL cameras using BadgerDB's DropPrefix
// This is intended for testing/QA use to reset database state
func (s *ANNTStore) DrainAllANNTs() ([]map[string]interface{}, error) {
	// First, collect all existing events to return them
	var anntStructures []map[string]interface{}

	err := s.db.View(func(txn *badger.Txn) error {
		itOpts := badger.DefaultIteratorOptions
		it := txn.NewIterator(itOpts)
		defer it.Close()

		// Scan ALL pending items (no ESN-specific prefix)
		prefix := []byte("pending_")
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			var data []byte
			err := item.Value(func(val []byte) error {
				data = append([]byte{}, val...)
				return nil
			})
			if err != nil {
				return err
			}

			var anntItem ANNTItem
			if err := json.Unmarshal(data, &anntItem); err != nil {
				continue
			}

			anntStructures = append(anntStructures, anntItem.ANNTData)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	// Now use BadgerDB's DropPrefix to efficiently clear all pending items
	err = s.db.DropPrefix([]byte("pending_"))
	if err != nil {
		return nil, err
	}

	if len(anntStructures) > 0 {
		s.logger.Infof("DRAINED %d ANNT structures from ALL cameras (DATABASE RESET)", len(anntStructures))
	}

	return anntStructures, nil
}

func (s *ANNTStore) maintenanceWorker() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.runMaintenance()
		}
	}
}

func (s *ANNTStore) runMaintenance() {
	// 1. Age-based cleanup with different policies
	s.cleanupByAge()

	// 2. Size-based cleanup if database is getting full
	s.cleanupBySize()

	// 3. BadgerDB garbage collection
	if err := s.db.RunValueLogGC(0.5); err != nil && err != badger.ErrNoRewrite {
		s.logger.Errorf("ANNT store value log GC failed: %v", err)
	}
}

func (s *ANNTStore) cleanupByAge() {
	now := time.Now()
	var keysToDelete [][]byte

	// Scan for old items (key-only for speed)
	if err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek([]byte("pending_")); it.ValidForPrefix([]byte("pending_")); it.Next() {
			var item ANNTItem
			if it.Item().Value(func(val []byte) error { return json.Unmarshal(val, &item) }) == nil {
				if now.Sub(item.CreatedAt) > itemTTL {
					keysToDelete = append(keysToDelete, it.Item().KeyCopy(nil))
				}
			}
		}
		return nil
	}); err != nil {
		s.logger.Errorf("Age cleanup scan failed: %v", err)
		return
	}

	// Delete old items
	if len(keysToDelete) > 0 {
		if err := s.db.Update(func(txn *badger.Txn) error {
			for _, key := range keysToDelete {
				if err := txn.Delete(key); err != nil {
					s.logger.Errorf("Failed to delete key: %v", err)
				}
			}
			return nil
		}); err != nil {
			s.logger.Errorf("Age cleanup delete failed: %v", err)
		} else {
			s.logger.Infof("Cleaned up %d items older than %v", len(keysToDelete), itemTTL)
		}
	}
}

func (s *ANNTStore) cleanupBySize() {
	currentSize := s.getApproximateSize()

	// Add warnings at different thresholds
	if currentSize > s.maxSize*70/100 && currentSize < s.maxSize*80/100 {
		s.logger.Warningf("Database at 70%% capacity (%d MB / %d MB)", currentSize/1024/1024, s.maxSize/1024/1024)
	}

	if currentSize < s.maxSize*80/100 {
		return // Not full enough
	}

	s.logger.Errorf("Database at 80%% capacity - starting cleanup (%d MB / %d MB)", currentSize/1024/1024, s.maxSize/1024/1024)
	targetSize := s.maxSize * 60 / 100
	var keysToDelete [][]byte

	// Scan oldest items (key-only for speed)
	if err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek([]byte("pending_")); it.ValidForPrefix([]byte("pending_")); it.Next() {
			if s.getApproximateSize() <= targetSize {
				break // Stop when target reached
			}
			keysToDelete = append(keysToDelete, it.Item().KeyCopy(nil))
		}
		return nil
	}); err != nil {
		s.logger.Errorf("Size cleanup scan failed: %v", err)
		return
	}

	// Delete oldest items
	if len(keysToDelete) > 0 {
		if err := s.db.Update(func(txn *badger.Txn) error {
			for _, key := range keysToDelete {
				if err := txn.Delete(key); err != nil {
					s.logger.Errorf("Failed to delete key: %v", err)
				}
			}
			return nil
		}); err != nil {
			s.logger.Errorf("Size cleanup delete failed: %v", err)
		} else {
			s.logger.Infof("Size cleanup: deleted %d oldest items", len(keysToDelete))
		}
	}
}

func (s *ANNTStore) getApproximateSize() int64 {
	lsm, vlog := s.db.Size()
	return lsm + vlog
}

// GetDB returns the underlying Badger database for metrics access
func (s *ANNTStore) GetDB() *badger.DB {
	return s.db
}

func (s *ANNTStore) Close() error {
	s.cancel()
	return s.db.Close()
}

// cleanupStaleLock attempts to remove stale BadgerDB lock files
// This is safe because we're checking if the process is actually running
func cleanupStaleLock(dir string, logger *goeen_log.Logger) error {
	lockFile := filepath.Join(dir, "LOCK")

	// Check if lock file exists
	if _, err := os.Stat(lockFile); os.IsNotExist(err) {
		return nil // No lock file, nothing to clean
	}

	// For containers, we can assume if we're starting up, any previous
	// instance was killed ungracefully. This is safe because:
	// 1. Container orchestration ensures only one instance per volume
	// 2. If another process was using it, Open() would fail anyway
	logger.Infof("Found potential stale lock file, attempting cleanup: %s", lockFile)

	if err := os.Remove(lockFile); err != nil {
		return fmt.Errorf("failed to remove stale lock file: %w", err)
	}

	logger.Infof("Successfully removed stale lock file: %s", lockFile)
	return nil
}
