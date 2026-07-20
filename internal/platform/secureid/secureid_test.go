package secureid

import (
	"regexp"
	"testing"
)

func TestHexUsesRequestedEntropyAndEncoding(t *testing.T) {
	first, second := Hex(16), Hex(16)
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(first) {
		t.Fatalf("Hex(16) = %q, want 32 lower-case hexadecimal characters", first)
	}
	if first == second {
		t.Fatal("two independently generated identifiers unexpectedly matched")
	}
}
