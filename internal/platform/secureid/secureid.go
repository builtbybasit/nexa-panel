// Package secureid creates opaque identifiers from the operating system's
// cryptographically secure random source.
package secureid

import (
	"crypto/rand"
	"encoding/hex"
)

// Hex returns a lower-case hexadecimal identifier with byteCount bytes of
// entropy. Go 1.25's crypto/rand.Read always fills the buffer and never returns
// an error, so callers do not need duplicated impossible-error branches.
func Hex(byteCount int) string {
	value := make([]byte, byteCount)
	rand.Read(value)
	return hex.EncodeToString(value)
}
