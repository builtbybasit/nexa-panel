package identity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	totpPeriodSeconds = int64(30)
	totpDigits        = 6
)

var base32Encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func generateTOTPSecret() (string, error) {
	secret := make([]byte, sha1.Size)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate TOTP secret: %w", err)
	}
	return base32Encoding.EncodeToString(secret), nil
}

func provisioningURI(username, secret string) string {
	label := "Nexa Panel:" + username
	query := url.Values{
		"secret":    []string{secret},
		"issuer":    []string{"Nexa Panel"},
		"algorithm": []string{"SHA1"},
		"digits":    []string{strconv.Itoa(totpDigits)},
		"period":    []string{strconv.FormatInt(totpPeriodSeconds, 10)},
	}
	return (&url.URL{Scheme: "otpauth", Host: "totp", Path: "/" + label, RawQuery: query.Encode()}).String()
}

func validateTOTP(secret, code string, now time.Time, lastAcceptedStep int64) (int64, bool) {
	if len(code) != totpDigits {
		return 0, false
	}
	for _, character := range code {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	decoded, err := base32Encoding.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil || len(decoded) == 0 {
		return 0, false
	}
	currentStep := now.Unix() / totpPeriodSeconds
	for _, step := range []int64{currentStep, currentStep - 1, currentStep + 1} {
		if step <= lastAcceptedStep || step < 0 {
			continue
		}
		expected := generateTOTP(decoded, step, totpDigits)
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

func generateTOTP(secret []byte, counter int64, digits int) string {
	message := make([]byte, 8)
	binary.BigEndian.PutUint64(message, uint64(counter))
	mac := hmac.New(sha1.New, secret)
	_, _ = mac.Write(message)
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 0x0f
	value := (uint32(digest[offset])&0x7f)<<24 |
		uint32(digest[offset+1])<<16 |
		uint32(digest[offset+2])<<8 |
		uint32(digest[offset+3])
	modulus := uint32(1)
	for range digits {
		modulus *= 10
	}
	return fmt.Sprintf("%0*d", digits, value%modulus)
}

func generateRecoveryCodes(count int) ([]string, []string, error) {
	if count < 1 {
		return nil, nil, errors.New("recovery code count must be positive")
	}
	codes := make([]string, 0, count)
	hashes := make([]string, 0, count)
	for range count {
		raw := make([]byte, 8)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, fmt.Errorf("generate recovery code: %w", err)
		}
		encoded := strings.ToUpper(hex.EncodeToString(raw))
		code := strings.Join([]string{encoded[0:4], encoded[4:8], encoded[8:12], encoded[12:16]}, "-")
		codes = append(codes, code)
		hashes = append(hashes, hashRecoveryCode(code))
	}
	return codes, hashes, nil
}

func hashRecoveryCode(code string) string {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	digest := sha256.Sum256([]byte("nexa-recovery-v1:" + normalized))
	return hex.EncodeToString(digest[:])
}
