package identity

import (
	"testing"
	"time"
)

func TestGenerateTOTPMatchesRFC6238SHA1Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	vectors := []struct {
		timestamp int64
		code      string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}
	for _, vector := range vectors {
		counter := vector.timestamp / totpPeriodSeconds
		if got := generateTOTP(secret, counter, 8); got != vector.code {
			t.Fatalf("generateTOTP(%d) = %s, want %s", vector.timestamp, got, vector.code)
		}
	}
}

func TestValidateTOTPAcceptsSmallDriftAndRejectsReuse(t *testing.T) {
	secret := "JBSWY3DPEHPK3PXP"
	decoded, err := base32Encoding.DecodeString(secret)
	if err != nil {
		t.Fatalf("decode test secret: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)
	previousStep := now.Unix()/totpPeriodSeconds - 1
	code := generateTOTP(decoded, previousStep, totpDigits)
	acceptedStep, ok := validateTOTP(secret, code, now, 0)
	if !ok || acceptedStep != previousStep {
		t.Fatalf("validateTOTP = %d, %v", acceptedStep, ok)
	}
	if _, ok := validateTOTP(secret, code, now, acceptedStep); ok {
		t.Fatal("a previously accepted TOTP should not be reusable")
	}
}

func TestRecoveryCodesAreUniqueAndHashNormalized(t *testing.T) {
	codes, hashes, err := generateRecoveryCodes(10)
	if err != nil {
		t.Fatalf("generateRecoveryCodes returned an error: %v", err)
	}
	if len(codes) != 10 || len(hashes) != 10 {
		t.Fatalf("code/hash counts = %d/%d", len(codes), len(hashes))
	}
	seen := make(map[string]struct{}, len(hashes))
	for index, hash := range hashes {
		if _, exists := seen[hash]; exists {
			t.Fatalf("duplicate recovery hash at %d", index)
		}
		seen[hash] = struct{}{}
		if hashRecoveryCode(codes[index]) != hashRecoveryCode(codes[index][0:4]+codes[index][5:9]+codes[index][10:14]+codes[index][15:19]) {
			t.Fatal("recovery code normalization changed its hash")
		}
	}
}
