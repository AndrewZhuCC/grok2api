package audit

import "testing"

// The request_audits table enforces CHECK (status_code BETWEEN 100 AND 599).
// A sentinel outside that range is rejected at write time, which previously
// failed every interrupt audit, degraded the billing ledger, and surfaced to
// callers as "billing ledger unavailable". The status must stay storable.
func TestQualityGuardStatusSatisfiesAuditConstraint(t *testing.T) {
	if StatusQualityGuardInterrupt < 100 || StatusQualityGuardInterrupt > 599 {
		t.Fatalf("StatusQualityGuardInterrupt = %d, must be within the storable range 100..599", StatusQualityGuardInterrupt)
	}
}

// The status must not collide with a code an upstream can plausibly return,
// otherwise a real upstream failure would be indistinguishable at a glance.
func TestQualityGuardStatusIsNotAnOrdinaryUpstreamCode(t *testing.T) {
	ordinary := []int{200, 400, 401, 403, 404, 408, 409, 429, 500, 502, 503, 504}
	for _, code := range ordinary {
		if StatusQualityGuardInterrupt == code {
			t.Fatalf("StatusQualityGuardInterrupt must not reuse upstream code %d", code)
		}
	}
}

// A 2xx value would make interrupts count as successful requests in the audit
// success predicate, hiding them from failure metrics entirely.
func TestQualityGuardStatusIsNotSuccessRange(t *testing.T) {
	if StatusQualityGuardInterrupt >= 200 && StatusQualityGuardInterrupt < 300 {
		t.Fatalf("StatusQualityGuardInterrupt = %d must not fall in the 2xx success range", StatusQualityGuardInterrupt)
	}
}
