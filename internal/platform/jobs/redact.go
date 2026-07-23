package jobs

import (
	"encoding/json"
	"strings"
)

// redactedSecret replaces credential material in an API response. It is not an
// empty string so a reader can tell "withheld" apart from "this field was
// empty". Mirrors the sites module's plan redaction.
const redactedSecret = "[redacted]"

// credentialKeyFragments are matched case-insensitively as substrings of a JSON
// object key. Job requests are handler-specific and unversioned — a new handler
// can start carrying a password without anyone touching this package — so the
// match is deliberately broad and over-redacts (a "monkey" field would go too)
// rather than risk publishing a live credential through GET /api/v1/jobs.
var credentialKeyFragments = []string{
	"password", "passwd", "secret", "token", "key", "passphrase",
	"credential", "auth", "private", "signature", "salt", "session",
}

// redactJobForAPI returns a copy of a job whose request and result payloads have
// every credential-shaped field replaced. The stored rows keep the real values:
// handlers re-read the request on lease recovery, so redaction belongs on the
// response path only and must never write back into the caller's value.
func redactJobForAPI(job Job) Job {
	job.Request = redactRawJSON(job.Request)
	job.Result = redactRawJSON(job.Result)
	return job
}

func redactJobsForAPI(jobs []Job) []Job {
	redacted := make([]Job, len(jobs))
	for index, job := range jobs {
		redacted[index] = redactJobForAPI(job)
	}
	return redacted
}

// redactRawJSON is fail-closed: a payload that cannot be decoded or re-encoded
// is replaced wholesale rather than passed through unexamined.
func redactRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return json.RawMessage(`"` + redactedSecret + `"`)
	}
	encoded, err := json.Marshal(redactValue(decoded))
	if err != nil {
		return json.RawMessage(`"` + redactedSecret + `"`)
	}
	return json.RawMessage(encoded)
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for key, nested := range typed {
			if isCredentialKey(key) {
				redacted[key] = redactedSecret
				continue
			}
			redacted[key] = redactValue(nested)
		}
		return redacted
	case []any:
		redacted := make([]any, len(typed))
		for index, nested := range typed {
			redacted[index] = redactValue(nested)
		}
		return redacted
	default:
		return value
	}
}

func isCredentialKey(key string) bool {
	lowered := strings.ToLower(key)
	for _, fragment := range credentialKeyFragments {
		if strings.Contains(lowered, fragment) {
			return true
		}
	}
	return false
}
