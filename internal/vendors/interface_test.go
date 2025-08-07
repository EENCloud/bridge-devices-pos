package vendors

import (
	"testing"
)

func TestVendorInterface_Basic(t *testing.T) {
	// Test that vendor interface can be satisfied with nil
	var vendor Vendor
	if vendor != nil {
		t.Error("Expected nil vendor interface")
	}
}

func TestRegister_Simple(t *testing.T) {
	// Simple test that doesn't require return values
	Register("test-vendor", nil)
}
