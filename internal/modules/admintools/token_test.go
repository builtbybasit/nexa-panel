package admintools

import (
	"regexp"
	"testing"
)

// phpSessionIDSafe is the character set PHP accepts for an externally supplied
// session id (php_session_valid_key: [A-Za-z0-9,-]), minus the comma, which is
// not safe in a cookie value. A tool session token becomes phpMyAdmin's
// SignonSession cookie value, which phpMyAdmin feeds straight to session_id();
// any character outside this set makes PHP reject the id, start an empty session,
// find no signon credentials, and bounce the user to nexa-signon-failed.
var phpSessionIDSafe = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

func TestSecureTokenIsAValidPHPSessionID(t *testing.T) {
	// base64url (the previous encoding) contains '_', which PHP rejects as a
	// session id — so roughly half of all launches failed sign-on at random. Every
	// generated token must stay within the PHP-safe set, or the intermittent
	// phpMyAdmin sign-on failure returns.
	for i := 0; i < 2000; i++ {
		token, err := secureToken()
		if err != nil {
			t.Fatal(err)
		}
		if !phpSessionIDSafe.MatchString(token) {
			t.Fatalf("secureToken produced a PHP-session-invalid token: %q", token)
		}
		// It must also satisfy the operator's own launch-token validator, which the
		// agent applies to the session id before writing the sign-on session file.
		if !tokenLengthOK(token) {
			t.Fatalf("token length %d is outside the accepted range: %q", len(token), token)
		}
	}
}

func tokenLengthOK(token string) bool { return len(token) >= 16 && len(token) <= 64 }
