package circuitbreaker

import (
	"testing"
	"testing/synctest"
	"time"
)

func TestRecoveryPermitCompletion(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cb := New(Config{FailureThreshold: 1, Cooldown: time.Second})
				cb.RecordFailure()
				if cb.Acquire() != nil {
					t.Fatal("admitted a request during cooldown")
				}
				time.Sleep(time.Second)
				for i := 0; i < 3; i++ {
					if !cb.AllowRequest() || cb.State() != StateHalfOpen {
						t.Fatal("eligibility/state reads consumed a recovery probe")
					}
				}
				probe := cb.Acquire()
				if probe == nil || cb.Acquire() != nil || cb.AllowRequest() {
					t.Fatal("expected exactly one reserved probe")
				}
				switch outcome {
				case "success":
					probe.RecordSuccess()
					if cb.State() != StateClosed || cb.ConsecutiveFailures() != 0 {
						t.Fatal("successful probe did not close/reset circuit")
					}
				case "failure":
					// Measure cooldown from completion, not admission.
					time.Sleep(2 * time.Second)
					probe.RecordFailure()
					if cb.State() != StateOpen || cb.ConsecutiveFailures() != 2 || cb.Trips() != 2 {
						t.Fatal("failed probe did not reopen circuit")
					}
					time.Sleep(time.Second - time.Nanosecond)
					if cb.Acquire() != nil {
						t.Fatal("probe failure did not restart cooldown")
					}
					time.Sleep(time.Nanosecond)
				case "cancel":
					probe.Release()
					if cb.State() != StateHalfOpen || cb.ConsecutiveFailures() != 1 || cb.Trips() != 1 {
						t.Fatal("cancellation changed failure accounting")
					}
				}
				next := cb.Acquire()
				if next == nil {
					t.Fatal("completion leaked probe reservation")
				}
				defer next.Release()
				// Repeated completion cannot affect a later request.
				probe.Release()
				probe.RecordSuccess()
				probe.RecordFailure()
				if outcome != "success" && cb.AllowRequest() {
					t.Fatal("old completion released the new recovery probe")
				}
			})
		})
	}
}

func TestStaleRequestCannotCompleteNewRecovery(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cb := New(Config{FailureThreshold: 1, Cooldown: time.Second})
				old := cb.Acquire()
				cb.Acquire().RecordFailure()
				time.Sleep(time.Second)
				probe := cb.Acquire()
				if probe == nil {
					t.Fatal("expected recovery probe")
				}
				switch outcome {
				case "success":
					old.RecordSuccess()
				case "failure":
					old.RecordFailure()
				case "cancel":
					old.Release()
				}
				if cb.State() != StateHalfOpen || cb.ConsecutiveFailures() != 1 || cb.Acquire() != nil {
					t.Fatal("stale request changed the active recovery probe")
				}
				probe.RecordSuccess()
				if cb.State() != StateClosed {
					t.Fatal("current probe could not close circuit")
				}
			})
		})
	}
}

func TestResetInvalidatesRecoveryPermit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cb := New(Config{FailureThreshold: 1, Cooldown: time.Second})
		cb.RecordFailure()
		time.Sleep(time.Second)
		old := cb.Acquire()
		cb.Reset()
		cb.RecordFailure()
		time.Sleep(time.Second)
		probe := cb.Acquire()
		if old == nil || probe == nil {
			t.Fatal("expected recovery permits")
		}
		old.RecordSuccess()
		if cb.State() != StateHalfOpen || cb.Acquire() != nil {
			t.Fatal("completion before Reset changed the new probe")
		}
		probe.Release()
	})
}
