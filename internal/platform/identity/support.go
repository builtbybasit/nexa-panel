package identity

import (
	"crypto/rand"
	"encoding/hex"
)

func randomID(bytes int) string {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(value)
}
