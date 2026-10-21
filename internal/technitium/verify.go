package technitium

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
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

// LookupPTR returns the names ip reverse-resolves to, asking dnsHost
// directly (the same way Verify does). An address with no PTR record
// returns no names and no error.
func LookupPTR(ctx context.Context, dnsHost, ip string, timeout time.Duration) ([]string, error) {
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: timeout}
			return d.DialContext(ctx, network, net.JoinHostPort(dnsHost, "53"))
		},
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	names, err := resolver.LookupAddr(cctx, ip)
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reverse lookup of %s via %s: %w", ip, dnsHost, err)
	}
	for i, n := range names {
		names[i] = strings.TrimSuffix(n, ".")
	}
	return names, nil
}

// dnsReverseAddr returns the reverse-lookup name for ip, with a trailing dot.
func dnsReverseAddr(ip string) (string, error) {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "", errors.New("not an IP address")
	}
	if v4 := parsed.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa.", v4[3], v4[2], v4[1], v4[0]), nil
	}
	const hexDigits = "0123456789abcdef"
	var b strings.Builder
	for i := len(parsed) - 1; i >= 0; i-- {
		b.WriteByte(hexDigits[parsed[i]&0xf])
		b.WriteByte('.')
		b.WriteByte(hexDigits[parsed[i]>>4])
		b.WriteByte('.')
	}
	b.WriteString("ip6.arpa.")
	return b.String(), nil
}
