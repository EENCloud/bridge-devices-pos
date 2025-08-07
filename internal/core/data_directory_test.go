package core

import (
	"strings"
	"testing"
)

func TestGetDataDirectory(t *testing.T) {
	dir := GetDataDirectory()

	// Should return a non-empty string
	if dir == "" {
		t.Error("Expected non-empty data directory")
	}

	// Should contain "bridge-devices-pos" in the path
	if !strings.Contains(dir, "bridge-devices-pos") {
		t.Errorf("Expected data directory to contain 'bridge-devices-pos', got '%s'", dir)
	}
}
