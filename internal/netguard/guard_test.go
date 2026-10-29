package netguard

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
	"testing"
)

type fakeResolver map[string][]string

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	ips, ok := f[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	var out []netip.Addr
	for _, s := range ips {
		out = append(out, netip.MustParseAddr(s))
	}
	return out, nil
}

var resolver = fakeResolver{
	"shop.example":     {"93.184.216.34"},
	"img.shop.example": {"93.184.216.35"},
	"other.example":    {"93.184.216.36"},
	"internal.example": {"10.0.0.5"},
	"mixed.example":    {"93.184.216.37", "127.0.0.1"},
	"localhost":        {"127.0.0.1", "::1"},
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestCheckTarget_AllowlistMatchesSubdomains(t *testing.T) {
	g := New([]string{" Shop.Example "}, false, resolver)
	for _, s := range []string{"https://shop.example/", "https://img.shop.example/a"} {
		if err := g.CheckTarget(context.Background(), mustURL(t, s)); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	if err := g.CheckTarget(context.Background(), mustURL(t, "https://other.example/")); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("want ErrNotAllowed, got %v", err)
	}
}

func TestCheckTarget_EmptyAllowlistAllowsAnyPublicHost(t *testing.T) {
	g := New(nil, false, resolver)
	if err := g.CheckTarget(context.Background(), mustURL(t, "https://other.example/")); err != nil {
		t.Fatal(err)
	}
}

func TestCheckHost_RejectsNonPublicAddresses(t *testing.T) {
	g := New(nil, false, resolver)
	for _, h := range []string{"internal.example", "mixed.example", "localhost", "127.0.0.1", "[::1]", "169.254.169.254", "10.1.2.3", "::ffff:192.168.0.1"} {
		if err := g.CheckHost(context.Background(), h); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%s: want ErrNotAllowed, got %v", h, err)
		}
	}
	if err := g.CheckHost(context.Background(), "shop.example."); err != nil {
		t.Fatal(err)
	}
}

func TestCheckHost_ResolveFailureIsNotErrNotAllowed(t *testing.T) {
	err := New(nil, false, resolver).CheckHost(context.Background(), "unknown.example")
	if err == nil || errors.Is(err, ErrNotAllowed) {
		t.Fatalf("want a resolve error, got %v", err)
	}
}

func TestCheckHost_AllowPrivateSkipsAddressCheck(t *testing.T) {
	g := New([]string{"shop.example"}, true, resolver)
	if err := g.CheckHost(context.Background(), "localhost"); err != nil {
		t.Fatal(err)
	}
	if err := g.CheckTarget(context.Background(), mustURL(t, "http://localhost/")); !errors.Is(err, ErrNotAllowed) {
		t.Fatal("the allowlist still applies when private targets are allowed")
	}
}

func TestIsPublic(t *testing.T) {
	cases := map[string]bool{
		"93.184.216.34": true, "2606:2800:220:1::1": true,
		"0.0.0.0": false, "100.64.0.1": false, "172.16.0.1": false, "192.168.1.1": false, "198.18.0.1": false,
		"224.0.0.1": false, "255.255.255.255": false, "fc00::1": false, "fe80::1": false, "64:ff9b::a00:1": false,
	}
	for s, want := range cases {
		if got := IsPublic(netip.MustParseAddr(s)); got != want {
			t.Errorf("%s: want %v, got %v", s, want, got)
		}
	}
	if IsPublic(netip.Addr{}) {
		t.Error("zero Addr must not be public")
	}
}

func TestNewWithNilResolverUsesDefault(t *testing.T) {
	if New(nil, false, nil).resolver == nil {
		t.Fatal("resolver must default")
	}
}
