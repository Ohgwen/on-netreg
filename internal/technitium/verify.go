package technitium

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"time"
)

// Verify performs a DNS lookup for fqdn directly against dnsHost (the
// Technitium server itself, bypassing any OS resolver cache) and confirms
// expectedIP is among the results. It's the "dig" verification step run
// after writing an Identity's shared DNS record, to confirm the write
// actually took effect and resolves as expected.
func Verify(ctx context.Context, dnsHost, fqdn, expectedIP string, timeout time.Duration) error {
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: timeout}
			return d.DialContext(ctx, network, net.JoinHostPort(dnsHost, "53"))
		},
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	addrs, err := resolver.LookupHost(cctx, fqdn)
	if err != nil {
		return fmt.Errorf("looking up %s via %s: %w", fqdn, dnsHost, err)
	}
	for _, a := range addrs {
		if a == expectedIP {
			return nil
		}
	}
	return fmt.Errorf("%s resolved to %v via %s, expected %s", fqdn, addrs, dnsHost, expectedIP)
}

// HostFromBaseURL extracts the hostname portion of a Technitium API base
// URL (e.g. "https://dns.example.com:8443" -> "dns.example.com"), on the
// assumption the server's DNS listener is reachable on the same host. If
// baseURL doesn't parse as a URL, it's returned unchanged so callers can
// still attempt a lookup (and get a clear error if it's wrong).
func HostFromBaseURL(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return baseURL
	}
	return u.Hostname()
}
