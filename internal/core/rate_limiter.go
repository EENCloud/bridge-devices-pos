package core

import (
	"math"
	"sync"
	"time"
)

type CircuitState int

const (
	CircuitClosed CircuitState = iota
	CircuitOpen
	CircuitHalfOpen
)

type AdaptiveRateLimiter struct {
	currentRate   float64
	maxRate       float64
	minRate       float64
	successWindow *SlidingWindow
	bridgeHealth  *HealthMonitor
	mutex         sync.RWMutex
	lastAdjust    time.Time
}

type SlidingWindow struct {
	requests []time.Time
	mutex    sync.RWMutex
	window   time.Duration
}

type HealthMonitor struct {
	successCount     int64
	failureCount     int64
	lastResponse     time.Time
	circuitState     CircuitState
	failureThreshold int
	recoveryTimeout  time.Duration
	mutex            sync.RWMutex
}

func NewAdaptiveRateLimiter(maxRate, minRate float64) *AdaptiveRateLimiter {
	return &AdaptiveRateLimiter{
		currentRate:   maxRate * 0.8, // Start at 80% of max
		maxRate:       maxRate,
		minRate:       minRate,
		successWindow: NewSlidingWindow(30 * time.Second),
		bridgeHealth:  NewHealthMonitor(5, 30*time.Second),
		lastAdjust:    time.Now(),
	}
}

func NewSlidingWindow(window time.Duration) *SlidingWindow {
	return &SlidingWindow{
		requests: make([]time.Time, 0),
		window:   window,
	}
}

func NewHealthMonitor(failureThreshold int, recoveryTimeout time.Duration) *HealthMonitor {
	return &HealthMonitor{
		failureThreshold: failureThreshold,
		recoveryTimeout:  recoveryTimeout,
		circuitState:     CircuitClosed,
	}
}

func (r *AdaptiveRateLimiter) CanServe() bool {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	// Check circuit breaker first
	if !r.bridgeHealth.CanProceed() {
		return false
	}

	// Clean old requests and check current rate
	r.successWindow.cleanup()
	currentRequestRate := r.successWindow.Rate()

	// Adaptive adjustment every 5 seconds
	if time.Since(r.lastAdjust) > 5*time.Second {
		r.adjustRate()
		r.lastAdjust = time.Now()
	}

	return currentRequestRate < r.currentRate
}

func (r *AdaptiveRateLimiter) adjustRate() {
	bridgeLoad := r.bridgeHealth.GetLoad()

	// Exponentially back off if bridge-core struggles
	if bridgeLoad > 0.8 {
		r.currentRate = math.Max(r.minRate, r.currentRate*0.5)
	} else if bridgeLoad < 0.3 {
		r.currentRate = math.Min(r.maxRate, r.currentRate*1.1)
	}
}

func (r *AdaptiveRateLimiter) RecordSuccess() {
	r.successWindow.Add(time.Now())
	r.bridgeHealth.RecordSuccess()
}

func (r *AdaptiveRateLimiter) RecordFailure() {
	r.bridgeHealth.RecordFailure()
}

func (r *AdaptiveRateLimiter) GetStats() map[string]interface{} {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	return map[string]interface{}{
		"current_rate":  r.currentRate,
		"max_rate":      r.maxRate,
		"min_rate":      r.minRate,
		"actual_rate":   r.successWindow.Rate(),
		"bridge_load":   r.bridgeHealth.GetLoad(),
		"circuit_state": r.bridgeHealth.GetCircuitState(),
		"success_count": r.bridgeHealth.successCount,
		"failure_count": r.bridgeHealth.failureCount,
	}
}

func (sw *SlidingWindow) Add(t time.Time) {
	sw.mutex.Lock()
	defer sw.mutex.Unlock()
	sw.requests = append(sw.requests, t)
}

func (sw *SlidingWindow) cleanup() {
	cutoff := time.Now().Add(-sw.window)
	validIndex := 0

	for i, t := range sw.requests {
		if t.After(cutoff) {
			validIndex = i
			break
		}
	}

	if validIndex > 0 {
		sw.requests = sw.requests[validIndex:]
	}
}

func (sw *SlidingWindow) Rate() float64 {
	sw.mutex.RLock()
	defer sw.mutex.RUnlock()

	if len(sw.requests) == 0 {
		return 0
	}

	duration := sw.window.Seconds()
	return float64(len(sw.requests)) / duration
}

func (hm *HealthMonitor) CanProceed() bool {
	hm.mutex.RLock()
	defer hm.mutex.RUnlock()

	switch hm.circuitState {
	case CircuitOpen:
		if time.Since(hm.lastResponse) > hm.recoveryTimeout {
			// Try to transition to half-open
			hm.circuitState = CircuitHalfOpen
			return true
		}
		return false
	case CircuitHalfOpen, CircuitClosed:
		return true
	default:
		return false
	}
}

func (hm *HealthMonitor) RecordSuccess() {
	hm.mutex.Lock()
	defer hm.mutex.Unlock()

	hm.successCount++
	hm.lastResponse = time.Now()

	if hm.circuitState == CircuitHalfOpen {
		hm.circuitState = CircuitClosed
		hm.failureCount = 0 // Reset failure count on recovery
	}
}

func (hm *HealthMonitor) RecordFailure() {
	hm.mutex.Lock()
	defer hm.mutex.Unlock()

	hm.failureCount++
	hm.lastResponse = time.Now()

	if hm.failureCount >= int64(hm.failureThreshold) {
		hm.circuitState = CircuitOpen
	}
}

func (hm *HealthMonitor) GetLoad() float64 {
	hm.mutex.RLock()
	defer hm.mutex.RUnlock()

	total := hm.successCount + hm.failureCount
	if total == 0 {
		return 0.0
	}

	failureRate := float64(hm.failureCount) / float64(total)

	// Load is failure rate + time since last response factor
	timeFactor := math.Min(1.0, time.Since(hm.lastResponse).Seconds()/30.0)

	return math.Min(1.0, failureRate+timeFactor*0.3)
}

func (hm *HealthMonitor) GetCircuitState() string {
	hm.mutex.RLock()
	defer hm.mutex.RUnlock()

	switch hm.circuitState {
	case CircuitClosed:
		return "CLOSED"
	case CircuitOpen:
		return "OPEN"
	case CircuitHalfOpen:
		return "HALF_OPEN"
	default:
		return "UNKNOWN"
	}
}
