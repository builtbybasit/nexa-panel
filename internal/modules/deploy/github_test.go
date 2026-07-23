package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	deployoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/deploy"
)

const (
	// testDeployPublicKey is what the node reports back for a site's deploy
	// key: the algorithm and body only, exactly as the observation carries it.
	testDeployPublicKey    = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	testDeployFingerprint  = "SHA256:0000000000000000000000000000000000000000000"
	testRotatedFingerprint = "SHA256:1111111111111111111111111111111111111111111"
	testRepository         = "git@github.com:acme/blog.git"
)

// The public half is the only thing worth storing, and the audit entry has to
// name which key was installed — that is the fingerprint, and nothing else.
func TestEnsureKeyStoresThePublicHalfAndAuditsIt(t *testing.T) {
	module, operator, _, auditLog, _ := newDeployModule(t)
	actor := "user_admin"
	key, err := module.ensureKey(context.Background(), testSite, testRepository, false, &actor, "203.0.113.9")
	if err != nil {
		t.Fatalf("ensureKey returned an error: %v", err)
	}
	if !key.Present || key.PublicKey != testDeployPublicKey || key.KeyVersion != 1 || key.Repository != testRepository {
		t.Fatalf("key = %+v", key)
	}
	if len(operator.ensured) != 1 || operator.ensured[0].Rotate || operator.ensured[0].UnixUser != testSite.UnixUser {
		t.Fatalf("ensure calls = %+v", operator.ensured)
	}
	recorded := findEvent(t, auditLog, "deploy.key_generated")
	if recorded.Metadata["fingerprint"] != testDeployFingerprint || recorded.Metadata["repository"] != testRepository {
		t.Fatalf("metadata = %+v", recorded.Metadata)
	}
	if recorded.ActorUserID == nil || *recorded.ActorUserID != actor || recorded.Subject != "site:site_blog" {
		t.Fatalf("actor = %v, subject = %q", recorded.ActorUserID, recorded.Subject)
	}
	for _, value := range recorded.Metadata {
		if text, ok := value.(string); ok && strings.Contains(text, "PRIVATE KEY") {
			t.Fatalf("private key material reached the audit metadata: %+v", recorded.Metadata)
		}
	}
}

// A repeated ensure must not look like a rotation: the key GitHub already
// trusts has not moved, so there is nothing to record and no version to bump.
func TestEnsureKeyIsIdempotentForAnUnchangedKey(t *testing.T) {
	module, _, _, auditLog, _ := newDeployModule(t)
	actor := "user_admin"
	if _, err := module.ensureKey(context.Background(), testSite, testRepository, false, &actor, ""); err != nil {
		t.Fatal(err)
	}
	key, err := module.ensureKey(context.Background(), testSite, "", false, &actor, "")
	if err != nil {
		t.Fatalf("second ensureKey returned an error: %v", err)
	}
	if key.KeyVersion != 1 || key.Repository != testRepository {
		t.Fatalf("key = %+v", key)
	}
	events, err := auditLog.List(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	generated := 0
	for _, event := range events {
		if event.Action == "deploy.key_generated" || event.Action == "deploy.key_rotated" {
			generated++
		}
	}
	if generated != 1 {
		t.Fatalf("an unchanged key produced %d key events: %+v", generated, events)
	}
}

// Rotation invalidates the key already registered on GitHub, so it gets its own
// audit action, a new version, and a cleared verdict.
func TestEnsureKeyRecordsARotation(t *testing.T) {
	module, operator, _, auditLog, _ := newDeployModule(t)
	actor := "user_admin"
	if _, err := module.ensureKey(context.Background(), testSite, testRepository, false, &actor, ""); err != nil {
		t.Fatal(err)
	}
	if err := module.recordTestOutcome(context.Background(), testSite.ID, true); err != nil {
		t.Fatal(err)
	}
	operator.fingerprint = testRotatedFingerprint
	key, err := module.ensureKey(context.Background(), testSite, "", true, &actor, "")
	if err != nil {
		t.Fatalf("rotation returned an error: %v", err)
	}
	if key.KeyVersion != 2 || key.Fingerprint != testRotatedFingerprint {
		t.Fatalf("key = %+v", key)
	}
	if key.LastTestedAt != nil || key.LastTestOK != nil {
		t.Fatalf("a rotated key kept the old verdict: %+v", key)
	}
	if !operator.ensured[len(operator.ensured)-1].Rotate {
		t.Fatalf("the node was not asked to rotate: %+v", operator.ensured)
	}
	recorded := findEvent(t, auditLog, "deploy.key_rotated")
	if recorded.Metadata["fingerprint"] != testRotatedFingerprint {
		t.Fatalf("metadata = %+v", recorded.Metadata)
	}
}

// An https remote would authenticate with a token or anonymously and so would
// prove nothing about the deploy key. It is refused before a job exists.
func TestEnsureKeyRejectsANonSSHRemote(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	actor := "user_admin"
	_, err := module.ensureKey(context.Background(), testSite, "https://github.com/acme/blog.git", false, &actor, "")
	var refused *refusal
	if !errors.As(err, &refused) || refused.code != "invalid_repository" {
		t.Fatalf("ensureKey err = %v, want an invalid_repository refusal", err)
	}
	if len(operator.ensured) != 0 {
		t.Fatalf("a rejected remote reached the node: %+v", operator.ensured)
	}
}

// The test is queued as a scoped job so the developer who started it can watch
// it, and its persisted payload must carry an identity and a repository only.
func TestTestGitHubSubmitsAScopedJob(t *testing.T) {
	module, _, _, _, _ := newDeployModule(t)
	actor := "user_admin"
	if _, err := module.ensureKey(context.Background(), testSite, testRepository, false, &actor, ""); err != nil {
		t.Fatal(err)
	}
	job, err := module.testGitHub(context.Background(), testSite, "", &actor, "203.0.113.9")
	if err != nil {
		t.Fatalf("testGitHub returned an error: %v", err)
	}
	if job.Kind != "deploy.github_test" || len(job.SiteIDs) != 1 || job.SiteIDs[0] != testSite.ID {
		t.Fatalf("job = %+v", job)
	}
	var payload githubTestPayload
	if err := json.Unmarshal(job.Request, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Repository != testRepository || payload.UnixUser != testSite.UnixUser {
		t.Fatalf("payload = %+v", payload)
	}
	if strings.Contains(string(job.Request), testDeployPublicKey) || strings.Contains(string(job.Request), "PRIVATE") {
		t.Fatalf("the job payload carries key material: %s", job.Request)
	}
}

// Testing access for a site that has no key is a mistake worth naming, not a
// job that fails on the node a minute later.
func TestTestGitHubRefusesWithoutAKey(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	actor := "user_admin"
	_, err := module.testGitHub(context.Background(), testSite, testRepository, &actor, "")
	var refused *refusal
	if !errors.As(err, &refused) || refused.code != "deploy_key_missing" {
		t.Fatalf("testGitHub err = %v, want a deploy_key_missing refusal", err)
	}
	if len(operator.tested) != 0 {
		t.Fatalf("the node was probed without a key: %+v", operator.tested)
	}
}

// The page renders the staged messages as they arrive and the tail at the end,
// so the progress has to move forward inside the range the worker accepts.
func TestGitHubTestJobReportsStagedProgress(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	actor := "user_admin"
	if _, err := module.ensureKey(context.Background(), testSite, testRepository, false, &actor, ""); err != nil {
		t.Fatal(err)
	}
	operator.result = deployoperator.GitHubTestResult{
		AuthOK: true, Account: "acme/blog", LsRemoteOK: true, RefCount: 3,
		OutputTail: "Hi acme/blog! You've successfully authenticated",
	}
	raw, err := json.Marshal(githubTestPayload{
		SiteID: testSite.ID, Slug: testSite.Slug, UnixUser: testSite.UnixUser,
		RootPath: testSite.RootPath, Repository: testRepository,
	})
	if err != nil {
		t.Fatal(err)
	}
	var progress []int
	report := func(next int, message string) error {
		if message == "" {
			return errors.New("job progress message is required")
		}
		if next < 0 || next > 99 || (len(progress) > 0 && next < progress[len(progress)-1]) {
			return errors.New("job progress must move forward between 0 and 99")
		}
		progress = append(progress, next)
		return nil
	}
	result, err := module.githubTestJob(context.Background(), raw, report)
	if err != nil {
		t.Fatalf("githubTestJob returned an error: %v", err)
	}
	verdict, ok := result.(deployoperator.GitHubTestResult)
	if !ok || verdict.RefCount != 3 || verdict.OutputTail == "" {
		t.Fatalf("result = %+v", result)
	}
	if len(progress) != 4 || progress[0] != 10 || progress[len(progress)-1] != 95 {
		t.Fatalf("progress = %v", progress)
	}
	key, err := module.deployKey(context.Background(), testSite)
	if err != nil {
		t.Fatal(err)
	}
	if key.LastTestedAt == nil || key.LastTestOK == nil || !*key.LastTestOK {
		t.Fatalf("the verdict was not stamped on the key: %+v", key)
	}
}

// A refused key is a verdict, not a broken job: the output tail is the reason
// the test was run, and a failed job keeps no result to render it from.
func TestGitHubTestJobReturnsAFailedProbeAsAResult(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	actor := "user_admin"
	if _, err := module.ensureKey(context.Background(), testSite, testRepository, false, &actor, ""); err != nil {
		t.Fatal(err)
	}
	operator.result = deployoperator.GitHubTestResult{OutputTail: "git@github.com: Permission denied (publickey)."}
	raw, err := json.Marshal(githubTestPayload{
		SiteID: testSite.ID, Slug: testSite.Slug, UnixUser: testSite.UnixUser,
		RootPath: testSite.RootPath, Repository: testRepository,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := module.githubTestJob(context.Background(), raw, func(int, string) error { return nil })
	if err != nil {
		t.Fatalf("githubTestJob returned an error: %v", err)
	}
	if verdict, ok := result.(deployoperator.GitHubTestResult); !ok || verdict.AuthOK || verdict.OutputTail == "" {
		t.Fatalf("result = %+v", result)
	}
	key, err := module.deployKey(context.Background(), testSite)
	if err != nil {
		t.Fatal(err)
	}
	if key.LastTestOK == nil || *key.LastTestOK {
		t.Fatalf("a refused key was recorded as working: %+v", key)
	}
}
