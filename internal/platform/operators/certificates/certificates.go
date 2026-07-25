package certificates

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/naming"
	"github.com/nexa-panel/nexa-panel/internal/platform/secureid"
)

const PlanKind = "nexa.certificate.v1"

type Request struct {
	CertificateID string   `json:"certificateId"`
	Operation     string   `json:"operation"`
	PrimaryDomain string   `json:"primaryDomain"`
	Domains       []string `json:"domains"`
	Email         string   `json:"email,omitempty"`
}
type Plan struct {
	ID              string    `json:"id"`
	Kind            string    `json:"kind"`
	Request         Request   `json:"request"`
	CertificatePath string    `json:"certificatePath"`
	PrivateKeyPath  string    `json:"privateKeyPath"`
	PlannedAt       time.Time `json:"plannedAt"`
	ExpiresAt       time.Time `json:"expiresAt"`
	Signature       string    `json:"signature,omitempty"`
}
type Observation struct {
	CertificateID   string    `json:"certificateId"`
	PrimaryDomain   string    `json:"primaryDomain"`
	Domains         []string  `json:"domains"`
	CertificatePath string    `json:"certificatePath"`
	PrivateKeyPath  string    `json:"privateKeyPath"`
	IssuedAt        time.Time `json:"issuedAt,omitempty"`
	ExpiresAt       time.Time `json:"expiresAt,omitempty"`
	Revoked         bool      `json:"revoked"`
}
type Operator interface {
	Plan(context.Context, Request) (Plan, error)
	Execute(context.Context, Plan) (Observation, error)
}
type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}
type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type HostOperator struct {
	runner   CommandRunner
	webroot  string
	liveRoot string
	now      func() time.Time
}

func NewHostOperator(runner CommandRunner, webroot, liveRoot string) (*HostOperator, error) {
	if runner == nil {
		runner = execRunner{}
	}
	if webroot == "" {
		webroot = "/srv/nexa/acme"
	}
	if liveRoot == "" {
		liveRoot = "/etc/letsencrypt/live"
	}
	if !filepath.IsAbs(webroot) || !filepath.IsAbs(liveRoot) {
		return nil, errors.New("certificate roots must be absolute")
	}
	return &HostOperator{runner: runner, webroot: filepath.Clean(webroot), liveRoot: filepath.Clean(liveRoot), now: time.Now}, nil
}

func (o *HostOperator) Plan(_ context.Context, request Request) (Plan, error) {
	request.PrimaryDomain = normalize(request.PrimaryDomain)
	for index := range request.Domains {
		request.Domains[index] = normalize(request.Domains[index])
	}
	sort.Strings(request.Domains)
	request.Domains = unique(request.Domains)
	if err := validate(request); err != nil {
		return Plan{}, err
	}
	now := o.now().UTC()
	root := filepath.Join(o.liveRoot, request.PrimaryDomain)
	return Plan{ID: randomID(), Kind: PlanKind, Request: request, CertificatePath: filepath.Join(root, "fullchain.pem"), PrivateKeyPath: filepath.Join(root, "privkey.pem"), PlannedAt: now, ExpiresAt: now.Add(30 * time.Minute)}, nil
}

func (o *HostOperator) Execute(ctx context.Context, plan Plan) (Observation, error) {
	if plan.ID == "" || plan.Kind != PlanKind || o.now().UTC().After(plan.ExpiresAt) {
		return Observation{}, errors.New("certificate plan is invalid or expired")
	}
	expected, err := o.Plan(ctx, plan.Request)
	if err != nil {
		return Observation{}, err
	}
	if expected.CertificatePath != plan.CertificatePath || expected.PrivateKeyPath != plan.PrivateKeyPath {
		return Observation{}, errors.New("certificate plan contains unmanaged paths")
	}
	if err := os.MkdirAll(o.webroot, 0o755); err != nil {
		return Observation{}, err
	}
	var args []string
	switch plan.Request.Operation {
	case "issue":
		args = []string{"certonly", "--webroot", "--webroot-path", o.webroot, "--cert-name", plan.Request.PrimaryDomain, "--non-interactive", "--agree-tos", "--email", plan.Request.Email, "--keep-until-expiring"}
		for _, domain := range plan.Request.Domains {
			args = append(args, "-d", domain)
		}
	case "renew":
		args = []string{"renew", "--cert-name", plan.Request.PrimaryDomain, "--webroot-path", o.webroot, "--non-interactive"}
	case "revoke":
		args = []string{"revoke", "--cert-name", plan.Request.PrimaryDomain, "--non-interactive", "--delete-after-revoke"}
	default:
		return Observation{}, errors.New("unsupported certificate operation")
	}
	output, err := o.runner.Run(ctx, "certbot", args...)
	if err != nil {
		message := strings.TrimSpace(string(output))
		if len(message) > 500 {
			message = message[:500]
		}
		return Observation{}, fmt.Errorf("certbot: %w: %s", err, message)
	}
	if plan.Request.Operation == "revoke" {
		return Observation{CertificateID: plan.Request.CertificateID, PrimaryDomain: plan.Request.PrimaryDomain, Domains: plan.Request.Domains, Revoked: true}, nil
	}
	return o.inspect(plan)
}

func (o *HostOperator) inspect(plan Plan) (Observation, error) {
	encoded, err := os.ReadFile(plan.CertificatePath)
	if err != nil {
		return Observation{}, err
	}
	block, _ := pem.Decode(encoded)
	if block == nil {
		return Observation{}, errors.New("issued certificate is not PEM")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return Observation{}, err
	}
	if _, err := os.Stat(plan.PrivateKeyPath); err != nil {
		return Observation{}, err
	}
	return Observation{CertificateID: plan.Request.CertificateID, PrimaryDomain: plan.Request.PrimaryDomain, Domains: certificate.DNSNames, CertificatePath: plan.CertificatePath, PrivateKeyPath: plan.PrivateKeyPath, IssuedAt: certificate.NotBefore.UTC(), ExpiresAt: certificate.NotAfter.UTC()}, nil
}

func validate(request Request) error {
	if request.CertificateID == "" || !naming.ValidHostname(request.PrimaryDomain) {
		return errors.New("certificate identity and primary domain are required")
	}
	if request.Operation != "issue" && request.Operation != "renew" && request.Operation != "revoke" {
		return errors.New("certificate operation is invalid")
	}
	containsPrimary := false
	for _, domain := range request.Domains {
		if domain == request.PrimaryDomain {
			containsPrimary = true
		}
	}
	if len(request.Domains) == 0 || !containsPrimary {
		return errors.New("certificate domains must include the primary domain")
	}
	for _, domain := range request.Domains {
		if !naming.ValidHostname(domain) || strings.HasPrefix(domain, "*.") {
			return errors.New("HTTP-01 certificate domains must be non-wildcard hostnames")
		}
	}
	if request.Operation == "issue" {
		address, err := mail.ParseAddress(request.Email)
		if err != nil || address.Address != request.Email {
			return errors.New("a valid ACME contact email is required")
		}
	}
	return nil
}

func normalize(value string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
}

func unique(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}
func randomID() string { return secureid.Hex(16) }
