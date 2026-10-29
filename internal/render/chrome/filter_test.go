package chrome

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"

	"github.com/otaqq/kagemusha/internal/render"
)

var bg = context.Background()

func TestFilter_BlockListMatchesSubdomains(t *testing.T) {
	f := newFilter(render.Request{BlockDomains: []string{" Google-Analytics.com ", "hotjar.com"}})
	if !f.active() {
		t.Fatal("active")
	}
	for _, u := range []string{"https://www.google-analytics.com/collect", "https://static.hotjar.com/x.js"} {
		if !f.blocked(bg, u) {
			t.Errorf("%s should be blocked", u)
		}
	}
	for _, u := range []string{"https://shop.example/app.js", "https://notgoogle-analytics.com/x", "::bad::"} {
		if f.blocked(bg, u) {
			t.Errorf("%s should pass", u)
		}
	}
}

func TestFilter_AllowListBlocksEverythingElse(t *testing.T) {
	f := newFilter(render.Request{BlockDomains: []string{"cdn.example"}, AllowDomains: []string{"shop.example", "api.example"}})
	if f.blocked(bg, "https://shop.example/") || f.blocked(bg, "https://img.shop.example/a.png") || f.blocked(bg, "https://api.example/v1") {
		t.Fatal("allowed hosts must pass")
	}
	if !f.blocked(bg, "https://www.googletagmanager.com/gtm.js") || !f.blocked(bg, "https://cdn.example/x") {
		t.Fatal("non-allowed hosts must be blocked")
	}
}

func TestFilter_NoListsIsInactive(t *testing.T) {
	f := newFilter(render.Request{AllowDomains: []string{" "}})
	if f.active() || f.blocked(bg, "https://anything.example/") {
		t.Fatal("inactive filter must pass everything")
	}
}

type countingGuard struct {
	deny  map[string]bool
	calls map[string]int
}

func (g *countingGuard) CheckHost(_ context.Context, host string) error {
	g.calls[host]++
	if g.deny[host] {
		return errors.New("denied")
	}
	return nil
}

func TestFilter_GuardBlocksAndCachesPerHost(t *testing.T) {
	g := &countingGuard{deny: map[string]bool{"internal.example": true}, calls: map[string]int{}}
	f := newFilter(render.Request{Guard: g})
	if !f.active() {
		t.Fatal("a guard alone must activate interception")
	}
	for range 3 {
		if !f.blocked(bg, "http://internal.example/x") {
			t.Fatal("guard-denied host must be blocked")
		}
		if f.blocked(bg, "https://shop.example/a.js") {
			t.Fatal("guard-allowed host must pass")
		}
	}
	if g.calls["internal.example"] != 1 || g.calls["shop.example"] != 1 {
		t.Fatalf("each host must be checked once per render: %v", g.calls)
	}
}

func headerMap(entries []*fetch.HeaderEntry) map[string]string {
	m := map[string]string{}
	for _, e := range entries {
		m[e.Name] = e.Value
	}
	return m
}

func TestFilter_RequestHeadersOnlyForTargetOriginAndForwardHosts(t *testing.T) {
	f := newFilter(render.Request{
		URL:          "https://Shop.Example:443/items/1",
		Headers:      map[string]string{"Authorization": "Basic dGVzdDp0ZXN0cGFzcw=="},
		ForwardHosts: []string{"api.example"},
	})
	if !f.active() {
		t.Fatal("forwarded headers must activate interception")
	}
	current := network.Headers{"Accept": "*/*", "authorization": "stale", "X-Num": 1}
	got := headerMap(f.requestHeaders("https://shop.example/app.js", current))
	if got["Authorization"] != "Basic dGVzdDp0ZXN0cGFzcw==" || got["Accept"] != "*/*" || len(got) != 2 {
		t.Fatalf("same origin must get merged headers: %v", got)
	}
	if got := f.requestHeaders("https://v2.api.example/v1", nil); len(got) != 1 {
		t.Fatalf("forward host must get headers: %v", got)
	}
	for _, u := range []string{"http://shop.example/", "https://shop.example:8443/", "https://img.shop.example/a.png", "https://tracker.example/", "::bad::"} {
		if f.requestHeaders(u, current) != nil {
			t.Errorf("%s must not get forwarded headers", u)
		}
	}
}

func TestFilter_NoHeadersLeavesRequestsUntouched(t *testing.T) {
	if newFilter(render.Request{URL: "https://shop.example/"}).requestHeaders("https://shop.example/", nil) != nil {
		t.Fatal("no headers configured")
	}
	if f := newFilter(render.Request{URL: "%zz", Headers: map[string]string{"A": "b"}}); f.active() {
		t.Fatal("an unparsable target URL must not enable forwarding")
	}
}

func TestOrigin(t *testing.T) {
	cases := map[string]string{
		"https://Shop.Example/x":   "https://shop.example",
		"https://shop.example:443": "https://shop.example",
		"http://shop.example:80/":  "http://shop.example",
		"http://127.0.0.1:8080/":   "http://127.0.0.1:8080",
		"http://[::1]:8080/":       "http://[::1]:8080",
	}
	for in, want := range cases {
		u, _ := url.Parse(in)
		if got := origin(u); got != want {
			t.Errorf("%s: want %s, got %s", in, want, got)
		}
	}
}

func TestFlagsFromArgs(t *testing.T) {
	opts := flagsFromArgs([]string{"--headless=new", "--no-sandbox", "--remote-debugging-port=0", "--", ""})
	if len(opts) != 2 {
		t.Fatalf("want 2 flags, got %d", len(opts))
	}
}

func TestFilter_BlocksConfiguredResourceTypes(t *testing.T) {
	f := newFilter(render.Request{BlockResourceTypes: []string{"Image", "Font"}})
	if !f.active() {
		t.Fatal("resource types alone must activate interception")
	}
	if !f.blockedType(network.ResourceTypeImage) || !f.blockedType(network.ResourceTypeFont) {
		t.Fatal("configured types must be blocked")
	}
	if f.blockedType(network.ResourceTypeDocument) || f.blockedType(network.ResourceTypeScript) || f.blockedType(network.ResourceTypeXHR) {
		t.Fatal("other types must pass")
	}
	if newFilter(render.Request{}).blockedType(network.ResourceTypeImage) {
		t.Fatal("nothing configured, nothing blocked")
	}
}
