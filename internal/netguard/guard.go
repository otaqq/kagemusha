// Package netguard decides which hosts kagemusha may render and fetch (SSRF protection).
// The target host must match the allowlist (when set), and no host may resolve to a
// private, loopback, link-local or otherwise non-public address unless explicitly allowed.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// ErrNotAllowed is returned when a host is outside the allowlist or resolves to a non-public address.
var ErrNotAllowed = errors.New("netguard: host not allowed")

// Resolver resolves a host name to addresses (net.DefaultResolver in production).
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Guard holds the target allowlist and the address policy.
type Guard struct {
	targets      []string
	allowPrivate bool
	resolver     Resolver
}

// New creates a Guard. Empty targets allows any public host. A nil resolver uses net.DefaultResolver.
func New(targets []string, allowPrivate bool, r Resolver) *Guard {
	if r == nil {
		r = net.DefaultResolver
	}
	return &Guard{targets: Normalize(targets), allowPrivate: allowPrivate, resolver: r}
}

// CheckTarget validates the URL to render: the host must be in the allowlist and resolve to public addresses.
func (g *Guard) CheckTarget(ctx context.Context, u *url.URL) error {
	host := strings.ToLower(u.Hostname())
	if len(g.targets) > 0 && !MatchDomain(host, g.targets) {
		return fmt.Errorf("%w: %s is not a target host", ErrNotAllowed, host)
	}
	return g.CheckHost(ctx, host)
}

// CheckHost validates any host the browser connects to (sub-requests and navigations included).
func (g *Guard) CheckHost(ctx context.Context, host string) error {
	if g.allowPrivate {
		return nil
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if addr, err := netip.ParseAddr(host); err == nil {
		return checkAddr(host, addr)
	}
	addrs, err := g.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("netguard: resolve %s: %w", host, err)
	}
	for _, a := range addrs {
		if err := checkAddr(host, a); err != nil {
			return err
		}
	}
	return nil
}

func checkAddr(host string, a netip.Addr) error {
	if !IsPublic(a) {
		return fmt.Errorf("%w: %s resolves to non-public address %s", ErrNotAllowed, host, a)
	}
	return nil
}

var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64 can reach IPv4 private ranges
}

// IsPublic reports whether a is a globally routable unicast address.
func IsPublic(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsUnspecified() || a.IsMulticast() ||
		a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// Normalize lowercases and trims a domain list, dropping empty entries.
func Normalize(list []string) []string {
	var out []string
	for _, d := range list {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" {
			out = append(out, d)
		}
	}
	return out
}

// MatchDomain reports whether host equals one of domains or is a subdomain of it.
func MatchDomain(host string, domains []string) bool {
	for _, d := range domains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}
