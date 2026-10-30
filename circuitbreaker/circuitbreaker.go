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
	probeInFlight       bool
	generation          uint64
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

// AllowRequest checks eligibility without reserving a recovery probe.
// Use Acquire when dispatching a request.
func (cb *CircuitBreaker) AllowRequest() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.stateLocked() != StateOpen && !cb.probeInFlight
}

// Permit tracks an admitted request. Finish it with RecordSuccess, RecordFailure,
// or Release on cancellation. Only the first completion takes effect.
type Permit struct {
	cb         *CircuitBreaker
	generation uint64
	once       sync.Once
}

// Acquire admits a request, reserving the sole probe if the circuit is half-open.
// It returns nil when the backend is unavailable.
func (cb *CircuitBreaker) Acquire() *Permit {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.stateLocked() == StateOpen || cb.probeInFlight {
		return nil
	}
	cb.probeInFlight = cb.state == StateHalfOpen
	return &Permit{cb: cb, generation: cb.generation}
}

// RecordSuccess completes this request successfully.
func (p *Permit) RecordSuccess() { p.finish(p.cb.recordSuccessLocked) }

// RecordFailure completes this request with an upstream failure.
func (p *Permit) RecordFailure() { p.finish(p.cb.recordFailureLocked) }

// Release cancels this request's reservation without changing failure counts.
func (p *Permit) Release() { p.finish(nil) }

func (p *Permit) finish(record func()) {
	p.once.Do(func() {
		p.cb.mu.Lock()
		defer p.cb.mu.Unlock()
		// Requests admitted before a state transition cannot complete a newer probe.
		if p.generation != p.cb.generation {
			return
		}
		p.cb.probeInFlight = false
		if record != nil {
			record()
		}
	})
}

// RecordSuccess records a successful request, resetting failures and closing the circuit.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.recordSuccessLocked()
}

func (cb *CircuitBreaker) recordSuccessLocked() {
	cb.consecutiveFailures = 0
	if cb.state != StateClosed {
		cb.setStateLocked(StateClosed)
	}
}

// RecordFailure records an upstream failure (e.g. 502, timeout, dial failure).
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.recordFailureLocked()
}

func (cb *CircuitBreaker) recordFailureLocked() {
	cb.consecutiveFailures++
	now := time.Now()

	switch cb.state {
	case StateHalfOpen:
		cb.setStateLocked(StateOpen)
		cb.trips++
	case StateClosed:
		if cb.consecutiveFailures >= cb.config.FailureThreshold {
			cb.setStateLocked(StateOpen)
			cb.trips++
		}
	case StateOpen:
		cb.lastStateChange = now
	}
}

// State returns the current State, transitioning from Open to HalfOpen if Cooldown has elapsed.
func (cb *CircuitBreaker) State() State {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.stateLocked()
}

func (cb *CircuitBreaker) stateLocked() State {
	if cb.state == StateOpen && time.Since(cb.lastStateChange) >= cb.config.Cooldown {
		cb.setStateLocked(StateHalfOpen)
	}
	return cb.state
}

func (cb *CircuitBreaker) setStateLocked(state State) {
	cb.state = state
	cb.lastStateChange = time.Now()
	cb.probeInFlight = false
	cb.generation++
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
	cb.setStateLocked(StateClosed)
	cb.consecutiveFailures = 0
}
