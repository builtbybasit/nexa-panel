package preflight

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// errTrustStore marks an endpoint that answered on the wire but whose
// certificate could not be verified. scripts/install.sh runs this check before
// it installs ca-certificates — deliberately, because the gate has to precede
// every mutation — so on a minimal rootfs (docker, LXC, debootstrap) https
// verification fails on a host whose network is perfectly healthy. Reachability
// is what this check exists to prove, and a TCP connection proves it.
var errTrustStore = errors.New("certificate not verifiable (no trust store yet)")

// endpointProbeTimeout bounds one endpoint fetch. It is a ceiling, not a
// reservation: the report's own budget still applies, and a probe cut short by
// that budget is reported as unchecked rather than as an unreachable host.
const endpointProbeTimeout = 5 * time.Second

// Endpoint is a host scripts/install.sh depends on. Required endpoints are the
// ones the install cannot proceed without: apt has nothing to install from and
// add-apt-repository cannot fetch a signing key when they are unreachable.
type Endpoint struct {
	Label    string
	Host     string
	URL      string
	Required bool
}

// DefaultEndpoints is the network surface of scripts/install.sh: Ubuntu's own
// archive, the two repositories the installer configures, and the Node.js
// release index the Applications catalog enumerates from.
func DefaultEndpoints() []Endpoint {
	return []Endpoint{
		{Label: "Ubuntu archive", Host: "archive.ubuntu.com", URL: "http://archive.ubuntu.com/ubuntu/dists/noble/Release", Required: true},
		{Label: "PHP repository (ppa:ondrej/php)", Host: "ppa.launchpadcontent.net", URL: "https://ppa.launchpadcontent.net/ondrej/php/ubuntu/dists/noble/Release", Required: true},
		{Label: "PostgreSQL repository (PGDG)", Host: "apt.postgresql.org", URL: "https://apt.postgresql.org/pub/repos/apt/dists/noble-pgdg/Release", Required: true},
		// The catalog degrades to the apt-provided runtimes when nodejs.org is
		// unreachable, so this one costs versions rather than the install.
		{Label: "Node.js release index", Host: "nodejs.org", URL: "https://nodejs.org/dist/index.json"},
	}
}

// NetworkCheck proves the host can resolve and reach the package sources before
// the installer starts running apt against them, so an offline or DNS-broken
// node fails with the reason rather than a wall of apt errors half way through.
type NetworkCheck struct {
	Endpoints []Endpoint
	Resolve   func(ctx context.Context, host string) error
	Reach     func(ctx context.Context, url string) error
}

func NewNetworkCheck(endpoints []Endpoint) NetworkCheck {
	return NetworkCheck{Endpoints: endpoints, Resolve: resolveHost, Reach: reachURL}
}

func (c NetworkCheck) Name() string { return "network" }

func (c NetworkCheck) Run(ctx context.Context) Result {
	const title = "Network"
	var required, optional, untrusted, expired []string
	for _, endpoint := range c.Endpoints {
		err := c.Resolve(ctx, endpoint.Host)
		if err == nil && endpoint.URL != "" {
			err = c.Reach(ctx, endpoint.URL)
		}
		if err == nil {
			continue
		}
		failure := fmt.Sprintf("%s: %v", endpoint.Label, err)
		switch {
		case errors.Is(err, errTrustStore):
			untrusted = append(untrusted, failure)
		// The report's budget running out says nothing about this host's
		// network: the probe was cut short, it did not observe a failure. A
		// genuinely offline host fails its own per-endpoint deadline while the
		// budget still has time left, and is still blocked below.
		case ctx.Err() != nil:
			expired = append(expired, failure)
		case endpoint.Required:
			required = append(required, failure)
		default:
			optional = append(optional, failure)
		}
	}

	if len(required) > 0 {
		return blocker(
			c.Name(), title,
			strings.Join(required, "; "),
			"The installer downloads every package it installs. Fix DNS, the default route, or the outbound proxy — an offline install cannot proceed.",
		)
	}
	if len(expired) > 0 {
		unchecked := append(append(append([]string{}, expired...), untrusted...), optional...)
		return warning(
			c.Name(), title,
			strings.Join(unchecked, "; "),
			"The report ran out of time before these answered, which is not evidence that the host is offline. Re-run `nexa doctor` with a longer --timeout to check them.",
		)
	}
	if len(untrusted) > 0 {
		return warning(
			c.Name(), title,
			strings.Join(append(untrusted, optional...), "; "),
			"These hosts answered but their certificates could not be checked; the installer installs ca-certificates before it fetches anything.",
		)
	}
	if len(optional) > 0 {
		return warning(
			c.Name(), title,
			strings.Join(optional, "; "),
			"The Applications catalog will offer only what this node's apt repositories publish.",
		)
	}
	return ok(c.Name(), title, fmt.Sprintf("%d package sources reachable", len(c.Endpoints)))
}

func resolveHost(ctx context.Context, host string) error {
	addresses, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(addresses) == 0 {
		return fmt.Errorf("resolve %s: no addresses", host)
	}
	return nil
}

func reachURL(ctx context.Context, target string) error {
	return probe{do: http.DefaultClient.Do, dial: dialAddress}.reach(ctx, target)
}

// probe is one endpoint fetch and the TCP fallback it degrades to. The two
// transports are fields so the classification below can be tested without a
// listening socket or a real name to resolve.
type probe struct {
	do   func(request *http.Request) (*http.Response, error)
	dial func(ctx context.Context, address string) error
}

func (p probe) reach(ctx context.Context, target string) error {
	ctx, cancel := context.WithTimeout(ctx, endpointProbeTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
	if err != nil {
		return fmt.Errorf("request %s: %w", target, err)
	}
	response, err := p.do(request)
	if err != nil {
		if !isTrustError(err) {
			return fmt.Errorf("reach %s: %w", target, err)
		}
		// Verification failed rather than the connection: fall back to the TCP
		// layer so a host that is merely missing ca-certificates is told that,
		// while a genuinely offline host still fails here.
		if dialErr := p.dial(ctx, addressOf(request.URL)); dialErr != nil {
			return fmt.Errorf("reach %s: %w", target, dialErr)
		}
		return fmt.Errorf("reach %s: %w", target, errTrustStore)
	}
	// Any status proves the packet got there and back, which is all this check
	// claims. Mirrors and CDNs routinely answer 403 or 405 to a HEAD probe, and
	// calling that "offline" would block the install on a healthy host.
	response.Body.Close()
	return nil
}

// isTrustError separates "I could not verify this certificate" from every other
// transport failure. Only the former is survivable before ca-certificates lands.
func isTrustError(err error) bool {
	var verification *tls.CertificateVerificationError
	if errors.As(err, &verification) {
		return true
	}
	var authority x509.UnknownAuthorityError
	return errors.As(err, &authority)
}

func addressOf(target *url.URL) string {
	port := target.Port()
	if port == "" {
		port = "443"
		if target.Scheme == "http" {
			port = "80"
		}
	}
	return net.JoinHostPort(target.Hostname(), port)
}

func dialAddress(ctx context.Context, address string) error {
	var dialer net.Dialer
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("connect %s: %w", address, err)
	}
	return connection.Close()
}
