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
