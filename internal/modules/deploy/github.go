package deploy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	deployoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/deploy"
)

// repositoryPattern restates the operator's own allowlist so a malformed
// remote is refused in the request that typed it rather than in a job that
// runs a minute later. An https:// remote is rejected here for the same reason
// the node rejects it: it would authenticate with a token or not at all, and
// this test exists to prove the deploy key works.
var repositoryPattern = regexp.MustCompile(`^git@github\.com:[A-Za-z0-9._-]{1,64}/[A-Za-z0-9._-]{1,100}(\.git)?$`)

// githubTestPayload is the job request. It is persisted verbatim
// (jobs/repository.go:107), so it carries only the identity the operator
// re-derives on the node and the repository the caller named — never key
// material of any kind.
type githubTestPayload struct {
	SiteID     string `json:"siteId"`
	Slug       string `json:"slug"`
	UnixUser   string `json:"unixUser"`
	RootPath   string `json:"rootPath"`
	Repository string `json:"repository"`
}

// deployKey reports the stored key for a site, or an absent one when the site
// has never been given a deploy key.
func (m *Module) deployKey(ctx context.Context, site sites.Site) (DeployKey, error) {
	row, err := m.deployKeyRow(ctx, site.ID)
	if err != nil {
		return DeployKey{}, err
	}
	return presentDeployKey(site.ID, row), nil
}

// ensureKey settles the site's deploy key on the node and records what came
// back. The audit entry is written after the node answers, not before: the
// fingerprint is the only thing that identifies which key was installed, it is
// unknown until the node has minted it, and the entry is still fail-closed —
// an ensure that cannot be recorded returns an error and the panel never
// stores or shows the public half.
func (m *Module) ensureKey(ctx context.Context, site sites.Site, repository string, rotate bool, actor *string, remoteAddress string) (DeployKey, error) {
	repository, err := normalizeRepository(repository)
	if err != nil {
		return DeployKey{}, refuse(http.StatusBadRequest, "invalid_repository", err.Error())
	}
	existing, err := m.deployKeyRow(ctx, site.ID)
	if err != nil {
		return DeployKey{}, err
	}
	if repository == "" && existing != nil {
		repository = existing.Repository
	}
	observation, err := m.operator.EnsureDeployKey(ctx, deployoperator.DeployKeyRequest{
		Slug: site.Slug, UnixUser: site.UnixUser, RootPath: site.RootPath, Rotate: rotate,
	})
	if err != nil {
		return DeployKey{}, operatorRefusal(err)
	}
	row := nextDeployKeyRow(site.ID, existing, observation, repository, m.now().UTC())
	if action := deployKeyAction(existing, row); action != "" {
		metadata := map[string]any{"fingerprint": row.Fingerprint, "algorithm": row.Algorithm, "repository": row.Repository}
		if err := m.record(ctx, action, site, actor, remoteAddress, metadata); err != nil {
			return DeployKey{}, err
		}
	}
	if err := m.upsertDeployKey(ctx, row); err != nil {
		return DeployKey{}, err
	}
	return presentDeployKey(site.ID, row), nil
}

// testGitHub queues the access test. The test itself is read-only, but it is a
// job rather than a synchronous call because it talks to a third party over
// the network twice and the page streams its progress; the payload carries no
// secret, which is what makes a persisted request acceptable here.
func (m *Module) testGitHub(ctx context.Context, site sites.Site, repository string, actor *string, remoteAddress string) (jobs.Job, error) {
	repository, err := normalizeRepository(repository)
	if err != nil {
		return jobs.Job{}, refuse(http.StatusBadRequest, "invalid_repository", err.Error())
	}
	row, err := m.deployKeyRow(ctx, site.ID)
	if err != nil {
		return jobs.Job{}, err
	}
	if row == nil {
		return jobs.Job{}, refuse(http.StatusConflict, "deploy_key_missing", "Generate a deploy key for this site before testing GitHub access.")
	}
	if repository == "" {
		repository = row.Repository
	}
	if repository == "" {
		return jobs.Job{}, refuse(http.StatusBadRequest, "invalid_repository", "Name the repository to test, such as git@github.com:owner/name.git.")
	}
	// The named repository is remembered so the next test, and the page, do not
	// have to be told again.
	if repository != row.Repository {
		row.Repository = repository
		row.UpdatedAt = m.now().UTC()
		if err := m.upsertDeployKey(ctx, row); err != nil {
			return jobs.Job{}, err
		}
	}
	// The test changes nothing, so its entry is best-effort: a briefly
	// unwritable audit table must not stop an operator from diagnosing a
	// deployment. Submit is still where it is recorded, because that is the
	// only point a human is attached to the run.
	m.jobs.Audit().RecordBestEffort(ctx, audit.Entry{
		ActorUserID: actor, Action: "deploy.github_tested", Subject: "site:" + site.ID,
		RemoteAddress: remoteAddress,
		Metadata:      map[string]any{"fingerprint": row.Fingerprint, "repository": repository},
	})
	payload := githubTestPayload{
		SiteID: site.ID, Slug: site.Slug, UnixUser: site.UnixUser,
		RootPath: site.RootPath, Repository: repository,
	}
	// The site scope is what lets a developer-role caller watch the job they
	// just started (jobs/access.go:64-87).
	return m.jobs.SubmitTitledWithOptions(ctx, "deploy.github_test", "Test GitHub access for "+site.PrimaryDomain, payload,
		jobs.SubmitOptions{ActorUserID: actor, SiteIDs: []string{site.ID}})
}

// githubTestJob runs the two probes on the node and returns their verdict as
// the job result. A probe that ran and failed is a result, not a failure: the
// output tail is the reason an operator asked for the test, and a failed job
// keeps no result to render it from.
func (m *Module) githubTestJob(ctx context.Context, raw json.RawMessage, report func(int, string) error) (any, error) {
	var payload githubTestPayload
	if err := json.Unmarshal(raw, &payload); err != nil || payload.SiteID == "" || payload.Repository == "" {
		return nil, errors.New("invalid GitHub access test request")
	}
	_ = report(10, "Checking the GitHub host keys.")
	_ = report(35, "Authenticating with GitHub over SSH.")
	result, err := m.operator.TestGitHub(ctx, deployoperator.GitHubTestRequest{
		Slug: payload.Slug, UnixUser: payload.UnixUser, RootPath: payload.RootPath, Repository: payload.Repository,
	})
	if err != nil {
		return nil, err
	}
	_ = report(80, "Listing the repository's branches.")
	if err := m.recordTestOutcome(ctx, payload.SiteID, result.AuthOK && result.LsRemoteOK); err != nil {
		return nil, err
	}
	if !result.AuthOK {
		_ = report(95, "GitHub refused the deploy key.")
		return result, nil
	}
	if !result.LsRemoteOK {
		_ = report(95, "GitHub accepted the key but the repository could not be read.")
		return result, nil
	}
	_ = report(95, "GitHub access is verified.")
	return result, nil
}

// recordTestOutcome stamps the verdict on the key row so the page can say when
// access was last proven without re-running the test.
func (m *Module) recordTestOutcome(ctx context.Context, siteID string, ok bool) error {
	now := m.now().UTC()
	_, err := m.database.NewUpdate().Model((*deployKeyModel)(nil)).
		Set("last_tested_at = ?", now).Set("last_test_ok = ?", ok).Set("updated_at = ?", now).
		Where("site_id = ?", siteID).Exec(ctx)
	return err
}

func (m *Module) deployKeyRow(ctx context.Context, siteID string) (*deployKeyModel, error) {
	row := new(deployKeyModel)
	err := m.database.NewSelect().Model(row).Where("site_id = ?", siteID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

func (m *Module) upsertDeployKey(ctx context.Context, row *deployKeyModel) error {
	_, err := m.database.NewInsert().Model(row).On("CONFLICT (site_id) DO UPDATE").
		Set("algorithm = EXCLUDED.algorithm").Set("public_key = EXCLUDED.public_key").
		Set("fingerprint = EXCLUDED.fingerprint").Set("key_version = EXCLUDED.key_version").
		Set("repository = EXCLUDED.repository").Set("last_tested_at = EXCLUDED.last_tested_at").
		Set("last_test_ok = EXCLUDED.last_test_ok").Set("created_at = EXCLUDED.created_at").
		Set("updated_at = EXCLUDED.updated_at").Exec(ctx)
	return err
}

// nextDeployKeyRow folds an observation into the row to store. A key whose
// fingerprint is unchanged keeps its version and its last test result, because
// nothing about it moved; a new fingerprint bumps the version and drops the
// verdict, because the key GitHub trusts is no longer this one.
func nextDeployKeyRow(siteID string, existing *deployKeyModel, observation deployoperator.DeployKeyObservation, repository string, now time.Time) *deployKeyModel {
	row := &deployKeyModel{
		SiteID: siteID, Algorithm: observation.Algorithm, PublicKey: observation.PublicKey,
		Fingerprint: observation.Fingerprint, KeyVersion: 1, Repository: repository,
		CreatedAt: observation.CreatedAt.UTC(), UpdatedAt: now,
	}
	if existing == nil {
		return row
	}
	if existing.Fingerprint == row.Fingerprint {
		row.KeyVersion = existing.KeyVersion
		row.LastTestedAt, row.LastTestOK = existing.LastTestedAt, existing.LastTestOK
		return row
	}
	row.KeyVersion = existing.KeyVersion + 1
	return row
}

// deployKeyAction names what actually happened on the node, so the audit log
// distinguishes a first key from a rotation that invalidated the one GitHub
// already trusts. An ensure that found the same key again changed nothing and
// is not an event.
func deployKeyAction(existing *deployKeyModel, row *deployKeyModel) string {
	switch {
	case existing == nil:
		return "deploy.key_generated"
	case existing.Fingerprint != row.Fingerprint:
		return "deploy.key_rotated"
	default:
		return ""
	}
}

func presentDeployKey(siteID string, row *deployKeyModel) DeployKey {
	if row == nil {
		return DeployKey{SiteID: siteID}
	}
	createdAt := row.CreatedAt
	return DeployKey{
		SiteID: siteID, Present: true, Algorithm: row.Algorithm, PublicKey: row.PublicKey,
		Fingerprint: row.Fingerprint, KeyVersion: row.KeyVersion, Repository: row.Repository,
		LastTestedAt: row.LastTestedAt, LastTestOK: row.LastTestOK, CreatedAt: &createdAt,
	}
}

// normalizeRepository accepts an empty repository — the key is useful before
// one is chosen — and otherwise nothing but the SSH remote form.
func normalizeRepository(repository string) (string, error) {
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return "", nil
	}
	if !repositoryPattern.MatchString(repository) {
		return "", errors.New("repository must be an SSH GitHub remote such as git@github.com:owner/name.git")
	}
	return repository, nil
}
