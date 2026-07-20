package certificates

import (
	"context"
	"testing"
	"time"
)

type recordingRunner struct{ args []string }

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.args = append([]string{name}, args...)
	return nil, nil
}

func TestPlanAllowsHTTP01SANsAndRevokeUsesCertName(t *testing.T) {
	runner := new(recordingRunner)
	operator, err := NewHostOperator(runner, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	operator.now = func() time.Time { return time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC) }
	plan, err := operator.Plan(context.Background(), Request{CertificateID: "cert-1", Operation: "revoke", PrimaryDomain: "example.com", Domains: []string{"www.example.com", "example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := operator.Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Revoked || len(runner.args) < 4 || runner.args[0] != "certbot" || runner.args[1] != "revoke" {
		t.Fatalf("result=%+v args=%v", result, runner.args)
	}
}

func TestRenewUsesWebrootAndCertificateName(t *testing.T) {
	runner := new(recordingRunner)
	operator, err := NewHostOperator(runner, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := operator.Plan(context.Background(), Request{CertificateID: "cert-1", Operation: "renew", PrimaryDomain: "example.com", Domains: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Execute(context.Background(), plan); err == nil {
		// The fake runner does not create the renewed PEM, so inspection must fail
		// after the Certbot invocation has been recorded.
		t.Fatal("expected certificate inspection to fail")
	}
	want := []string{"certbot", "renew", "--cert-name", "example.com", "--webroot-path"}
	if len(runner.args) < len(want) {
		t.Fatalf("renew args = %v", runner.args)
	}
	for index, value := range want {
		if runner.args[index] != value {
			t.Fatalf("renew args = %v", runner.args)
		}
	}
}

func TestPlanRejectsWildcardAndMissingEmail(t *testing.T) {
	operator, _ := NewHostOperator(new(recordingRunner), t.TempDir(), t.TempDir())
	if _, err := operator.Plan(context.Background(), Request{CertificateID: "cert", Operation: "issue", PrimaryDomain: "example.com", Domains: []string{"example.com", "*.example.com"}, Email: "admin@example.com"}); err == nil {
		t.Fatal("expected wildcard rejection")
	}
	if _, err := operator.Plan(context.Background(), Request{CertificateID: "cert", Operation: "issue", PrimaryDomain: "example.com", Domains: []string{"example.com"}}); err == nil {
		t.Fatal("expected email rejection")
	}
}
