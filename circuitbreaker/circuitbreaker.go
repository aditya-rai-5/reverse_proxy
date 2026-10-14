package circuitbreaker

import (
	"sync"
	"time"
)

// State represents the state of a circuit breaker.
type State string

const (
	StateClosed   State = "closed"
	StateHalfOpen State = "half-open"
	StateOpen     State = "open"
)

type Config struct {
	FailureThreshold int           `yaml:"failure_threshold"`
	Cooldown         time.Duration `yaml:"cooldown"`
}

func (c *Config) Defaults() {
	if c.FailureThreshold <= 0 {
		c.FailureThreshold = 5
	}
	if c.Cooldown <= 0 {
		c.Cooldown = 10 * time.Second
	}
}

// CircuitBreaker implements the Circuit Breaker pattern to protect against repeatedly
// sending requests to failing backends.
type CircuitBreaker struct {
	mu                  sync.Mutex
	config              Config
	state               State
	consecutiveFailures int
	trips               uint64
	lastStateChange     time.Time
}

// New creates a new CircuitBreaker with the given configuration.
func New(cfg Config) *CircuitBreaker {
	cfg.Defaults()
	return &CircuitBreaker{
		config:          cfg,
		state:           StateClosed,
		lastStateChange: time.Now(),
	}
}

// AllowRequest returns true if a request is permitted according to current circuit state.
func (cb *CircuitBreaker) AllowRequest() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()
	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		if now.Sub(cb.lastStateChange) >= cb.config.Cooldown {
			cb.state = StateHalfOpen
			cb.lastStateChange = now
			return true
		}
		return false
	case StateHalfOpen:
		return true
	default:
		return true
	}
}

// RecordSuccess records a successful request, resetting failures and closing the circuit.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.consecutiveFailures = 0
	if cb.state != StateClosed {
		cb.state = StateClosed
		cb.lastStateChange = time.Now()
	}
}

// RecordFailure records an upstream failure (e.g. 502, timeout, dial failure).
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.consecutiveFailures++
	now := time.Now()

	switch cb.state {
	case StateHalfOpen:
		cb.state = StateOpen
		cb.trips++
		cb.lastStateChange = now
	case StateClosed:
		if cb.consecutiveFailures >= cb.config.FailureThreshold {
			cb.state = StateOpen
			cb.trips++
			cb.lastStateChange = now
		}
	case StateOpen:
		cb.lastStateChange = now
	}
}

// State returns the current State, transitioning from Open to HalfOpen if Cooldown has elapsed.
func (cb *CircuitBreaker) State() State {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == StateOpen && time.Since(cb.lastStateChange) >= cb.config.Cooldown {
		cb.state = StateHalfOpen
		cb.lastStateChange = time.Now()
	}
	return cb.state
}

// ConsecutiveFailures returns the current number of consecutive failures recorded.
func (cb *CircuitBreaker) ConsecutiveFailures() int {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.consecutiveFailures
}

// Trips returns the number of times this breaker has transitioned into Open.
func (cb *CircuitBreaker) Trips() uint64 {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.trips
}

// Reset clears failure counts and restores the circuit breaker to Closed.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = StateClosed
	cb.consecutiveFailures = 0
	cb.lastStateChange = time.Now()
}
