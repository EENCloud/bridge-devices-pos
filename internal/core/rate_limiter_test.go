package core

import (
	"testing"
	"time"
)

func TestNewSlidingWindow(t *testing.T) {
	window := NewSlidingWindow(time.Minute)
	if window == nil {
		t.Error("Expected non-nil sliding window")
	}
}

func TestNewHealthMonitor(t *testing.T) {
	monitor := NewHealthMonitor(5, 10)
	if monitor == nil {
		t.Error("Expected non-nil health monitor")
	}
}

func TestNewAdaptiveRateLimiter(t *testing.T) {
	limiter := NewAdaptiveRateLimiter(100, 60.0)
	if limiter == nil {
		t.Error("Expected non-nil rate limiter")
	}
}
