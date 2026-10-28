package chrome

import "testing"

func TestFilter_BlockListMatchesSubdomains(t *testing.T) {
	f := newFilter([]string{" Google-Analytics.com ", "hotjar.com"}, nil)
	if !f.active() {
		t.Fatal("active")
	}
	for _, u := range []string{"https://www.google-analytics.com/collect", "https://static.hotjar.com/x.js"} {
		if !f.blocked(u) {
			t.Errorf("%s should be blocked", u)
		}
	}
	for _, u := range []string{"https://shop.example/app.js", "https://notgoogle-analytics.com/x", "::bad::"} {
		if f.blocked(u) {
			t.Errorf("%s should pass", u)
		}
	}
}

func TestFilter_AllowListBlocksEverythingElse(t *testing.T) {
	f := newFilter([]string{"cdn.example"}, []string{"shop.example", "api.example"})
	if f.blocked("https://shop.example/") || f.blocked("https://img.shop.example/a.png") || f.blocked("https://api.example/v1") {
		t.Fatal("allowed hosts must pass")
	}
	if !f.blocked("https://www.googletagmanager.com/gtm.js") || !f.blocked("https://cdn.example/x") {
		t.Fatal("non-allowed hosts must be blocked")
	}
}

func TestFilter_NoListsIsInactive(t *testing.T) {
	f := newFilter(nil, []string{" "})
	if f.active() || f.blocked("https://anything.example/") {
		t.Fatal("inactive filter must pass everything")
	}
}

func TestFlagsFromArgs(t *testing.T) {
	opts := flagsFromArgs([]string{"--headless=new", "--no-sandbox", "--remote-debugging-port=0", "--", ""})
	if len(opts) != 2 {
		t.Fatalf("want 2 flags, got %d", len(opts))
	}
}
