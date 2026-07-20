package agent

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	nodeoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/nodes"
)

func (s *Server) signPlan(plan nodeoperator.Plan) string {
	plan.Signature = ""
	return signPayload(s.token, "node.probe.plan.v1", plan)
}

func (s *Server) verifyPlan(plan nodeoperator.Plan) bool {
	provided := plan.Signature
	plan.Signature = ""
	return verifyPayload(s.token, "node.probe.plan.v1", plan, provided)
}

func signPayload(token, domain string, payload any) string {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(domain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(encoded)
	return hex.EncodeToString(mac.Sum(nil))
}

func verifyPayload(token, domain string, payload any, provided string) bool {
	if len(provided) != sha256.Size*2 {
		return false
	}
	expected := signPayload(token, domain, payload)
	return expected != "" && hmac.Equal([]byte(provided), []byte(expected))
}
