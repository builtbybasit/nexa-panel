package agent

import (
	"context"
	"testing"

	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
)

// teardownPlanOperator issues a plan the way the host operator does, with the
// empty before-state that makes a rollback strip the site.
type teardownPlanOperator struct{}

func (teardownPlanOperator) Plan(_ context.Context, site siteoperator.Site) (siteoperator.Plan, error) {
	return siteoperator.Plan{
		ID: "plan-1", Kind: siteoperator.PlanKind, Site: site,
		Artifacts: []siteoperator.Artifact{{Kind: "nginx-site", Path: "/etc/nginx/sites-available/nexa-example.conf", Content: "x"}},
		Before: []siteoperator.Snapshot{
			{Path: "/etc/nginx/sites-available/nexa-example.conf", Exists: true, Digest: "abc123", Content: "x"},
		},
		Retired:       []string{"/etc/logrotate.d/nexa-example"},
		RetiredBefore: []siteoperator.Snapshot{{Path: "/etc/logrotate.d/nexa-example", Exists: true, Digest: "def456", Content: "y"}},
		EnabledBefore: true,
	}, nil
}

func (o teardownPlanOperator) PlanTeardown(ctx context.Context, site siteoperator.Site) (siteoperator.Plan, error) {
	plan, err := o.Plan(ctx, site)
	if err != nil {
		return siteoperator.Plan{}, err
	}
	plan.Before = []siteoperator.Snapshot{{Path: plan.Artifacts[0].Path}}
	plan.RetiredBefore = []siteoperator.Snapshot{{Path: plan.Retired[0]}}
	plan.EnabledBefore = false
	plan.Teardown = true
	return plan, nil
}

func (teardownPlanOperator) Apply(context.Context, siteoperator.Plan) (siteoperator.Observation, error) {
	return siteoperator.Observation{}, nil
}

func (teardownPlanOperator) Rollback(context.Context, siteoperator.Plan) (siteoperator.Observation, error) {
	return siteoperator.Observation{}, nil
}

// A site teardown sends the issued plan straight back to the agent, so the plan
// the agent signs must verify unchanged. This previously failed in production
// with "The site plan was not issued by this agent." because the control panel
// built the synthetic shape itself by editing an already-signed activation plan;
// the signature covers the whole plan, so any such edit invalidates it.
func TestTeardownPlanVerifiesAsIssued(t *testing.T) {
	server := &Server{token: "shared-token", sites: teardownPlanOperator{}}

	issued, err := server.sites.PlanTeardown(context.Background(), siteoperator.Site{ID: "site_1", Slug: "example"})
	if err != nil {
		t.Fatal(err)
	}
	issued.Signature = server.signSitePlan(issued)

	if !server.verifySitePlan(issued) {
		t.Fatal("the teardown plan the agent issued does not verify when sent back unchanged")
	}

	// The before-state really is blanked, or the rollback would restore the site
	// instead of removing it.
	if issued.Before[0].Exists || issued.RetiredBefore[0].Exists || issued.EnabledBefore {
		t.Fatalf("teardown plan does not carry an empty before-state: %+v", issued)
	}
}

// Guards the regression directly: editing a signed plan must be rejected, which
// is why the synthetic shape has to come from the agent rather than the caller.
func TestEditedSitePlanIsRejected(t *testing.T) {
	server := &Server{token: "shared-token", sites: teardownPlanOperator{}}
	plan, err := server.sites.Plan(context.Background(), siteoperator.Site{ID: "site_1", Slug: "example"})
	if err != nil {
		t.Fatal(err)
	}
	plan.Signature = server.signSitePlan(plan)

	plan.Before = []siteoperator.Snapshot{{Path: plan.Artifacts[0].Path}}
	plan.EnabledBefore = false

	if server.verifySitePlan(plan) {
		t.Fatal("an edited plan verified; the teardown regression could recur unnoticed")
	}
}

func (teardownPlanOperator) Purge(context.Context, siteoperator.Site) error { return nil }
