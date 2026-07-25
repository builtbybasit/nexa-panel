package agent

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

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

// signPlan clears a plan's own signature field and returns the signature over
// the remaining domain-tagged payload. sig must point at the Signature field of
// plan (the same value), so zeroing it is reflected in what gets marshaled —
// this centralises the clear-then-sign dance every operator plan shares.
func (s *Server) signPlan(domain string, sig *string, plan any) string {
	*sig = ""
	return signPayload(s.token, domain, plan)
}

// verifyPlan is the inverse of signPlan: it captures the provided signature,
// clears the field so the payload matches what signPlan hashed, then verifies.
func (s *Server) verifyPlan(domain string, sig *string, plan any) bool {
	provided := *sig
	*sig = ""
	return verifyPayload(s.token, domain, plan, provided)
}
