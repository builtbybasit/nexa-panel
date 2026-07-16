package agent

import (
	"crypto/sha256"

	"crypto/hmac"
	"encoding/json"
	"fmt"
	nodeoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/nodes"

	"crypto/subtle"
)

func (s *Server) signPlan(plan nodeoperator.Plan) string {
	plan.Signature = ""
	encoded, _ := json.Marshal(plan)
	mac := hmac.New(sha256.New, []byte(s.token))
	_, _ = mac.Write(encoded)
	return fmt.Sprintf("%x", mac.Sum(nil))
}

func (s *Server) verifyPlan(plan nodeoperator.Plan) bool {
	provided := plan.Signature
	expected := s.signPlan(plan)
	return len(provided) == len(expected) && subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}
