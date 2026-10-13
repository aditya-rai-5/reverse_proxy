package circuitbreaker

import (
	"sync"
	"testing"
	"time"
)

func TestCircuitBreakerInitialState(t *testing.T) {
	cb := New(Config{})

	if cb.State() != StateClosed {
		t.Errorf("Expected initial state %s, got %s", StateClosed, cb.State())
	}
	if !cb.AllowRequest() {
		t.Error("Expected AllowRequest to be true initially")
	}
	if cb.ConsecutiveFailures() != 0 {
		t.Errorf("Expected 0 consecutive failures, got %d", cb.ConsecutiveFailures())
	}
}

func TestCircuitBreakerTripAfterThreshold(t *testing.T) {
	cb := New(Config{
		FailureThreshold: 5,
		Cooldown:         50 * time.Millisecond,
	})

	for i := 1; i <= 4; i++ {
		cb.RecordFailure()
		if cb.State() != StateClosed {
			t.Fatalf("Failure %d: expected state %s, got %s", i, StateClosed, cb.State())
		}
		if !cb.AllowRequest() {
			t.Fatalf("Failure %d: expected AllowRequest to be true", i)
		}
		if cb.ConsecutiveFailures() != i {
			t.Fatalf("Expected %d failures, got %d", i, cb.ConsecutiveFailures())
		}
	}

	// 5th failure must trip the breaker to Open
	cb.RecordFailure()
	if cb.State() != StateOpen {
		t.Fatalf("Expected state %s after 5 failures, got %s", StateOpen, cb.State())
	}
	if cb.AllowRequest() {
		t.Error("Expected AllowRequest to be false when Open")
	}
	if cb.ConsecutiveFailures() != 5 {
		t.Errorf("Expected 5 failures, got %d", cb.ConsecutiveFailures())
	}
}

func TestCircuitBreakerSuccessResetsFailures(t *testing.T) {
	cb := New(Config{
		FailureThreshold: 5,
		Cooldown:         50 * time.Millisecond,
	})

	// 3 failures
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.ConsecutiveFailures() != 3 {
		t.Errorf("Expected 3 failures, got %d", cb.ConsecutiveFailures())
	}

	// 1 success resets counter to 0
	cb.RecordSuccess()
	if cb.ConsecutiveFailures() != 0 {
		t.Errorf("Expected 0 failures after success, got %d", cb.ConsecutiveFailures())
	}
	if cb.State() != StateClosed {
		t.Errorf("Expected state %s, got %s", StateClosed, cb.State())
	}
}

func TestCircuitBreakerHalfOpenRecovery(t *testing.T) {
	cb := New(Config{
		FailureThreshold: 3,
		Cooldown:         30 * time.Millisecond,
	})

	// Trip the breaker
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}
	if cb.State() != StateOpen {
		t.Fatalf("Expected state %s, got %s", StateOpen, cb.State())
	}

	// Immediate check: still Open
	if cb.AllowRequest() {
		t.Error("Expected AllowRequest to return false while in cooldown")
	}

	// Wait for cooldown to expire
	time.Sleep(35 * time.Millisecond)

	// Now state transitions to Half-Open and permits trial request
	if !cb.AllowRequest() {
		t.Error("Expected AllowRequest to be true after cooldown in Half-Open state")
	}
	if cb.State() != StateHalfOpen {
		t.Errorf("Expected state %s, got %s", StateHalfOpen, cb.State())
	}

	// Successful trial request recovers circuit to Closed
	cb.RecordSuccess()
	if cb.State() != StateClosed {
		t.Errorf("Expected state %s after successful recovery, got %s", StateClosed, cb.State())
	}
	if cb.ConsecutiveFailures() != 0 {
		t.Errorf("Expected 0 failures after recovery, got %d", cb.ConsecutiveFailures())
	}
}

func TestCircuitBreakerHalfOpenFailureTripsAgain(t *testing.T) {
	cb := New(Config{
		FailureThreshold: 3,
		Cooldown:         30 * time.Millisecond,
	})

	// Trip the breaker
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}
	time.Sleep(35 * time.Millisecond)

	// In Half-Open
	if !cb.AllowRequest() {
		t.Error("Expected AllowRequest to be true in Half-Open")
	}

	// Failed trial request trips back to Open immediately
	cb.RecordFailure()
	if cb.State() != StateOpen {
		t.Errorf("Expected state to trip back to %s on failed trial, got %s", StateOpen, cb.State())
	}
	if cb.AllowRequest() {
		t.Error("Expected AllowRequest to be false after re-trip to Open")
	}
}

func TestCircuitBreakerReset(t *testing.T) {
	cb := New(Config{
		FailureThreshold: 2,
		Cooldown:         10 * time.Second,
	})

	cb.RecordFailure()
	cb.RecordFailure()
	if cb.State() != StateOpen {
		t.Fatalf("Expected Open, got %s", cb.State())
	}

	cb.Reset()
	if cb.State() != StateClosed {
		t.Errorf("Expected Closed after reset, got %s", cb.State())
	}
	if cb.ConsecutiveFailures() != 0 {
		t.Errorf("Expected 0 failures after reset, got %d", cb.ConsecutiveFailures())
	}
	if !cb.AllowRequest() {
		t.Error("Expected AllowRequest to be true after reset")
	}
}

func TestCircuitBreakerConcurrency(t *testing.T) {
	cb := New(Config{
		FailureThreshold: 50,
		Cooldown:         10 * time.Millisecond,
	})

	var wg sync.WaitGroup
	const routines = 20
	const iterations = 100

	for i := 0; i < routines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = cb.AllowRequest()
				if (id+j)%3 == 0 {
					cb.RecordFailure()
				} else {
					cb.RecordSuccess()
				}
				_ = cb.State()
				_ = cb.ConsecutiveFailures()
			}
		}(i)
	}

	wg.Wait()
}
