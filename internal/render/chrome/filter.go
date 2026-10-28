package chrome

import (
	"net/url"
	"strings"
)

// filter holds the domain block / allow lists. When allow is set, everything outside it is blocked.
type filter struct {
	block []string
	allow []string
}

func newFilter(block, allow []string) *filter {
	return &filter{block: normalize(block), allow: normalize(allow)}
}

func normalize(list []string) []string {
	var out []string
	for _, d := range list {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" {
			out = append(out, d)
		}
	}
	return out
}

func (f *filter) active() bool { return len(f.block) > 0 || len(f.allow) > 0 }

// blocked reports whether the request URL should be blocked, matching on host name (subdomains included).
func (f *filter) blocked(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if len(f.allow) > 0 && !matchAny(host, f.allow) {
		return true
	}
	return matchAny(host, f.block)
}

func matchAny(host string, domains []string) bool {
	for _, d := range domains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}
