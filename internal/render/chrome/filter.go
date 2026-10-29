package chrome

import (
	"context"
	"net/url"
	"strings"
	"sync"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"

	"github.com/otaqq/kagemusha/internal/netguard"
	"github.com/otaqq/kagemusha/internal/render"
)

// filter is the per-render request policy applied through Fetch interception:
// the domain block / allow lists, blocked resource types, the host guard, and header forwarding.
// When allow is set, everything outside it is blocked.
type filter struct {
	block []string
	allow []string
	types map[network.ResourceType]bool

	guard   render.HostGuard
	mu      sync.Mutex
	checked map[string]bool // host → blocked, so each host is resolved once per render

	origin       string
	headers      map[string]string
	forwardHosts []string
}

func newFilter(req render.Request) *filter {
	f := &filter{
		block:        netguard.Normalize(req.BlockDomains),
		types:        map[network.ResourceType]bool{},
		allow:        netguard.Normalize(req.AllowDomains),
		guard:        req.Guard,
		checked:      map[string]bool{},
		forwardHosts: netguard.Normalize(req.ForwardHosts),
	}
	for _, t := range req.BlockResourceTypes {
		f.types[network.ResourceType(t)] = true
	}
	if len(req.Headers) > 0 {
		if u, err := url.Parse(req.URL); err == nil {
			f.origin = origin(u)
			f.headers = req.Headers
		}
	}
	return f
}

// active reports whether requests need to be intercepted at all.
func (f *filter) active() bool {
	return len(f.block) > 0 || len(f.allow) > 0 || len(f.types) > 0 || f.guard != nil || len(f.headers) > 0
}

// blockedType reports whether requests of this resource type are never fetched.
func (f *filter) blockedType(t network.ResourceType) bool { return f.types[t] }

// blocked reports whether the request URL should be blocked, matching on host name (subdomains included).
func (f *filter) blocked(ctx context.Context, rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if len(f.allow) > 0 && !netguard.MatchDomain(host, f.allow) {
		return true
	}
	if netguard.MatchDomain(host, f.block) {
		return true
	}
	return f.guardBlocks(ctx, host)
}

func (f *filter) guardBlocks(ctx context.Context, host string) bool {
	if f.guard == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.checked[host]; ok {
		return b
	}
	b := f.guard.CheckHost(ctx, host) != nil
	f.checked[host] = b
	return b
}

// requestHeaders returns the full header set to send when forwarded headers apply to rawURL, or nil to leave the request untouched.
// Headers go only to the target's origin and ForwardHosts, never to third parties.
func (f *filter) requestHeaders(rawURL string, current network.Headers) []*fetch.HeaderEntry {
	if len(f.headers) == 0 {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return nil
	}
	if origin(u) != f.origin && !netguard.MatchDomain(strings.ToLower(u.Hostname()), f.forwardHosts) {
		return nil
	}
	// Fetch.continueRequest replaces all headers, so start from the current ones
	var out []*fetch.HeaderEntry
	for name, v := range current {
		if _, override := lookupFold(f.headers, name); override {
			continue
		}
		if s, ok := v.(string); ok {
			out = append(out, &fetch.HeaderEntry{Name: name, Value: s})
		}
	}
	for name, v := range f.headers {
		out = append(out, &fetch.HeaderEntry{Name: name, Value: v})
	}
	return out
}

func lookupFold(m map[string]string, key string) (string, bool) {
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

// origin returns scheme://host[:port] with the default port omitted, as Chrome reports request URLs.
func origin(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if p := u.Port(); p != "" && !(scheme == "http" && p == "80") && !(scheme == "https" && p == "443") {
		host += ":" + p
	}
	return scheme + "://" + host
}
