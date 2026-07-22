package sites

import (
	"encoding/json"
	"strings"
	"testing"

	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
)

const testHash = "$2a$10$CUZtkt8pIfvnrwf91rMonOy3NXW6u88JPxqN8bTbHqsReNOFoT54O"

// planWithCredential builds a plan carrying the basic-auth hash on every route
// it can reach a response by: the settings field, the rendered password file,
// that file's before-snapshot, and the retired-path snapshot that survives after
// basic auth is switched off.
func planWithCredential() siteoperator.Plan {
	const htpasswd = "/etc/nginx/nexa-includes/nexa-demo.htpasswd"
	line := "demo:" + testHash + "\n"
	return siteoperator.Plan{
		ID: "plan-1", Kind: siteoperator.PlanKind,
		Site: siteoperator.Site{
			ID: "site_1", Slug: "demo",
			Settings: siteoperator.Settings{BasicAuth: siteoperator.BasicAuth{
				Enabled: true, Username: "demo", PasswordHash: testHash,
			}},
		},
		Artifacts: []siteoperator.Artifact{
			{Kind: "nginx-site", Path: "/etc/nginx/sites-available/nexa-demo.conf", Content: "server {}"},
			{Kind: "nginx-htpasswd", Path: htpasswd, Mode: 0o640, Content: line},
		},
		Before: []siteoperator.Snapshot{
			{Path: "/etc/nginx/sites-available/nexa-demo.conf", Exists: true, Digest: "aaa", Content: "server {}"},
			{Path: htpasswd, Exists: true, Digest: "bbb", Content: line},
		},
		Retired:       []string{"/etc/logrotate.d/nexa-demo", htpasswd},
		RetiredBefore: []siteoperator.Snapshot{{Path: "/etc/logrotate.d/nexa-demo"}, {Path: htpasswd, Exists: true, Digest: "ccc", Content: line}},
	}
}

// The plan endpoint answers to sites.read, which viewer and developer both hold,
// so no part of the response may carry the credential.
func TestRedactedPlanCarriesNoCredential(t *testing.T) {
	encoded, err := json.Marshal(redactPlanForAPI(planWithCredential()))
	if err != nil {
		t.Fatal(err)
	}
	if body := string(encoded); strings.Contains(body, testHash) {
		t.Fatalf("password hash survived redaction:\n%s", body)
	}
}

// Everything a reviewer actually needs must still be there: redaction must not
// quietly empty the plan.
func TestRedactedPlanKeepsReviewableDetail(t *testing.T) {
	redacted := redactPlanForAPI(planWithCredential())
	if len(redacted.Artifacts) != 2 || len(redacted.Before) != 2 || len(redacted.RetiredBefore) != 2 {
		t.Fatalf("redaction dropped entries: %+v", redacted)
	}
	if redacted.Artifacts[0].Content != "server {}" {
		t.Fatalf("non-secret artifact was redacted: %q", redacted.Artifacts[0].Content)
	}
	htpasswd := redacted.Artifacts[1]
	if htpasswd.Path == "" || htpasswd.Mode != 0o640 || htpasswd.Content != redactedSecret {
		t.Fatalf("password file entry = %+v, want path and mode kept with the body withheld", htpasswd)
	}
	if !redacted.Before[1].Exists {
		t.Fatal("redaction must not disguise whether the password file existed")
	}
	// A retired path that was never written stays untouched, so "absent" and
	// "withheld" remain distinguishable.
	if redacted.RetiredBefore[0].Content != "" || redacted.RetiredBefore[0].Digest != "" {
		t.Fatalf("an absent retired path was redacted: %+v", redacted.RetiredBefore[0])
	}
}

// The stored plan must keep the real credential: the node re-renders plan.Site
// and demands byte-exact artifact equality, so redacting in place would make
// every site with basic auth fail activation.
func TestRedactionDoesNotMutateTheStoredPlan(t *testing.T) {
	stored := planWithCredential()
	before, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	_ = redactPlanForAPI(stored)
	after, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("redaction wrote back into the caller's plan:\nbefore: %s\nafter:  %s", before, after)
	}
	if !strings.Contains(string(after), testHash) {
		t.Fatal("the stored plan lost its credential; activation would fail byte-exact validation")
	}
}
