package agent

import "testing"

func TestPlanSignaturesAreDomainSeparated(t *testing.T) {
	payload := map[string]string{"id": "same-payload"}
	first := signPayload("shared-token", "site.plan.v1", payload)
	second := signPayload("shared-token", "package.plan.v1", payload)
	if first == "" || second == "" || first == second {
		t.Fatalf("domain-separated signatures = %q and %q", first, second)
	}
	if verifyPayload("shared-token", "package.plan.v1", payload, first) {
		t.Fatal("signature from one operation domain verified in another")
	}
}
