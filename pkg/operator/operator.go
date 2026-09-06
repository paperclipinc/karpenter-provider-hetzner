package operator

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

// newHTTPClient returns the HTTP client the hcloud SDK is driven with.
//
// hcloud.NewClient defaults to a bare &http.Client{}, which has no Timeout and
// therefore no upper bound on a single request. A blackholed connection to
// api.hetzner.cloud (dropped SYN/ACK, a middlebox that accepts and never
// answers, a TLS handshake that stalls) parks the calling goroutine forever.
// Every call site here is a controller-runtime worker, and controller-runtime
// imposes no per-reconcile deadline, so a hung request permanently consumes a
// worker rather than failing and requeueing. Enough of them and the operator
// stops reconciling entirely while still reporting healthy.
//
// The timeout is per REQUEST, not per operation, which is what makes it safe to
// set aggressively: the SDK's action waiter polls /actions in a loop of separate
// requests, so a server create that legitimately takes two minutes is a long
// sequence of short requests, not one long one.
//
// A timeout also turns a hang into a retryable error rather than a silent stall:
// http.Client surfaces it as a net.Error with Timeout() == true, which the SDK's
// retry policy (5 attempts, exponential backoff capped at a minute) already
// treats as retryable, and which it abandons as soon as the caller's context is
// done.
//
// The transport bounds the phases individually as well. Client.Timeout alone
// covers the whole request, so without these a stalled TLS handshake is
// indistinguishable from a slow response and burns the entire budget before
// anything is retried.
func newHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = timeout
	// The operator talks to exactly one host, and the SDK's action waiter polls it
	// twice a second per in-flight create. The default of 2 idle connections per
	// host makes concurrent launches tear down and redial TLS constantly.
	transport.MaxIdleConnsPerHost = 20
	return &http.Client{Timeout: timeout, Transport: transport}
}

// hcloudOptions returns the SDK options used in production. Split out from
// NewHCloudClient so tests can point the same configuration at a test server.
func hcloudOptions(token string, timeout time.Duration) []hcloud.ClientOption {
	return []hcloud.ClientOption{
		hcloud.WithToken(token),
		hcloud.WithHTTPClient(newHTTPClient(timeout)),
		hcloud.WithApplication("karpenter-provider-hetzner", ""),
	}
}

// NewHCloudClient creates a new Hetzner Cloud API client using the HCLOUD_TOKEN
// environment variable and the API timeout from cfg.
func NewHCloudClient(cfg *Config) (*hcloud.Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	token := os.Getenv("HCLOUD_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("HCLOUD_TOKEN environment variable is required")
	}
	return hcloud.NewClient(hcloudOptions(token, cfg.APITimeout)...), nil
}
