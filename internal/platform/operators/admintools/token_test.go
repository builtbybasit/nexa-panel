package admintools

import "testing"

func TestValidTokenRejectsPHPSessionUnsafeCharacters(t *testing.T) {
	// A launch session id becomes phpMyAdmin's SignonSession cookie, which PHP
	// feeds to session_id(); PHP's valid-key set is [A-Za-z0-9,-], so '_' (and
	// anything else outside it) must be rejected here rather than silently
	// producing a session file phpMyAdmin can never read.
	rejected := []string{
		"has_underscore_0123456789",  // '_' — the base64url character that broke sign-on
		"has,comma,0123456789abcd",   // ',' — PHP-valid but unsafe in a cookie
		"has spaces 0123456789abcd",  // whitespace
		"has/slash/0123456789abcdef", // path separator
	}
	for _, value := range rejected {
		if validToken(value) {
			t.Errorf("validToken accepted a PHP-session-unsafe id: %q", value)
		}
	}
	accepted := []string{
		"onlyhyphen-and-alnum-0123",
		"PlainAlnum0123456789abcd",
		"deadbeefdeadbeefdeadbeefdeadbeef", // hex, the new token encoding
	}
	for _, value := range accepted {
		if !validToken(value) {
			t.Errorf("validToken rejected a valid session id: %q", value)
		}
	}
}
