package reminders

import (
	"testing"

	"hireloop/api/internal/resend"
)

func TestDeliveryCallbacksPreserveConfirmedEvidence(t *testing.T) {
	if transitionAllowed(StateDelivered, StateSuppressed) {
		t.Fatal("late suppression must not overwrite confirmed delivery")
	}
	if transitionAllowed(StateDelivered, StatePermanentFailed) {
		t.Fatal("late bounce must not overwrite confirmed delivery")
	}
	if !transitionAllowed(StateExhausted, StateDelivered) {
		t.Fatal("late delivery confirmation should upgrade exhausted")
	}
}

func TestDeliveryDelayedConfirmsAcceptanceWithoutRetry(t *testing.T) {
	tr, ok := TransitionForEvent(resend.EventDeliverySlow)
	if !ok {
		t.Fatal("delivery_delayed should update delivery state")
	}
	if tr.State != StateAccepted || tr.ScheduleRetry || tr.RetryAt != nil {
		t.Fatalf("delivery_delayed transition = %+v, want accepted without retry", tr)
	}
	if !transitionAllowed(StateUncertain, tr.State) {
		t.Fatal("provider acceptance should resolve an uncertain send")
	}
}
