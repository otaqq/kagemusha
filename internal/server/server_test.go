package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otaqq/kagemusha/internal/cache"
	"github.com/otaqq/kagemusha/internal/flow"
	"github.com/otaqq/kagemusha/internal/netguard"
	"github.com/otaqq/kagemusha/internal/render"
)

type fakeRenderer struct {
	healthy bool
	res     render.Result
	err     error
	delay   time.Duration
	mu      sync.Mutex
	calls   int
	reqs    []render.Request
}

func (f *fakeRenderer) Render(ctx context.Context, req render.Request) (render.Result, error) {
	f.mu.Lock()
	f.calls++
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return f.res, f.err
}
func (f *fakeRenderer) Healthy(ctx context.Context) bool { return f.healthy }

func newHandler(f *fakeRenderer, opt Options) http.Handler {
	if opt.QueueTimeout == 0 {
		opt.QueueTimeout = time.Second
	}
	return New(f, flow.New(1), opt, nil)
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHealthz_ReflectsEngineState(t *testing.T) {
	if rec := get(newHandler(&fakeRenderer{healthy: true}, Options{}), "/healthz"); rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("healthy: %d %v", rec.Code, rec.Header())
	}
	if rec := get(newHandler(&fakeRenderer{healthy: false}, Options{}), "/healthz"); rec.Code != 503 {
		t.Fatalf("unhealthy: %d", rec.Code)
	}
}

func TestRender_200SetsCacheControlAndDropsUpstreamHeaders(t *testing.T) {
	f := &fakeRenderer{healthy: true, res: render.Result{HTML: "<html>ok</html>", StatusCode: 200, Trigger: render.TriggerReady}}
	rec := get(newHandler(f, Options{CacheControl: "public, max-age=3600"}), "/render?url=https://shop.example/")
	if rec.Code != 200 || rec.Body.String() != "<html>ok</html>" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "public, max-age=3600" || rec.Header().Get("X-Kagemusha-Trigger") != "ready" {
		t.Fatalf("headers: %v", rec.Header())
	}
	if rec.Header().Get("ETag") != "" {
		t.Fatal("must not inherit upstream headers")
	}
}

func TestRender_Non200IsAlwaysNoStore(t *testing.T) {
	f := &fakeRenderer{healthy: true, res: render.Result{HTML: "nf", StatusCode: 404, Trigger: render.TriggerIdle}}
	rec := get(newHandler(f, Options{CacheControl: "public, max-age=3600"}), "/render?url=https://shop.example/missing")
	if rec.Code != 404 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
}

func TestRender_UndeclaredStatusIs200(t *testing.T) {
	f := &fakeRenderer{healthy: true, res: render.Result{HTML: "x"}}
	if rec := get(newHandler(f, Options{}), "/render?url=https://shop.example/"); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
}

func TestRender_InvalidURLIs400(t *testing.T) {
	h := newHandler(&fakeRenderer{healthy: true}, Options{})
	for _, p := range []string{"/render", "/render?url=ftp://x", "/render?url=%zz", "/render?url=https://"} {
		if rec := get(h, p); rec.Code != 400 {
			t.Fatalf("%s: %d", p, rec.Code)
		}
	}
}

func TestRender_DenyPathsReturn404(t *testing.T) {
	f := &fakeRenderer{healthy: true, res: render.Result{HTML: "x"}}
	rec := get(newHandler(f, Options{DenyPaths: []string{"/api"}}), "/render?url=https://shop.example/api/v1/x")
	if rec.Code != 404 || f.calls != 0 {
		t.Fatalf("%d calls=%d", rec.Code, f.calls)
	}
}

func TestRender_PrerenderCompatiblePath(t *testing.T) {
	f := &fakeRenderer{healthy: true, res: render.Result{HTML: "x"}}
	rec := get(newHandler(f, Options{}), "/https://shop.example/items?page=2")
	if rec.Code != 200 || f.calls != 1 {
		t.Fatalf("%d calls=%d", rec.Code, f.calls)
	}
}

func TestRender_RenderFailureIs502NoStore(t *testing.T) {
	f := &fakeRenderer{healthy: true, err: errors.New("browser died")}
	rec := get(newHandler(f, Options{CacheControl: "public"}), "/render?url=https://shop.example/")
	if rec.Code != 502 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
}

func TestRender_QueueTimeoutIs503(t *testing.T) {
	f := &fakeRenderer{healthy: true, res: render.Result{HTML: "x"}, delay: 200 * time.Millisecond}
	h := New(f, flow.New(1), Options{QueueTimeout: 30 * time.Millisecond}, nil)
	go get(h, "/render?url=https://shop.example/a")
	time.Sleep(10 * time.Millisecond)
	if rec := get(h, "/render?url=https://shop.example/b"); rec.Code != 503 {
		t.Fatalf("want 503, got %d", rec.Code)
	}
}

type fakeGuard struct{ err error }

func (g fakeGuard) CheckTarget(context.Context, *url.URL) error { return g.err }

func TestRender_GuardRejectsTargetsWithoutRendering(t *testing.T) {
	cases := []struct {
		err  error
		code int
	}{
		{fmt.Errorf("wrapped: %w", netguard.ErrNotAllowed), http.StatusForbidden},
		{errors.New("dns timeout"), http.StatusBadGateway},
	}
	for _, c := range cases {
		f := &fakeRenderer{healthy: true, res: render.Result{HTML: "x"}}
		rec := get(newHandler(f, Options{Guard: fakeGuard{c.err}, CacheControl: "public"}), "/render?url=http://169.254.169.254/latest/meta-data/")
		if rec.Code != c.code || f.calls != 0 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%v: %d calls=%d %v", c.err, rec.Code, f.calls, rec.Header())
		}
	}
	f := &fakeRenderer{healthy: true, res: render.Result{HTML: "x"}}
	if rec := get(newHandler(f, Options{Guard: fakeGuard{}}), "/render?url=https://shop.example/"); rec.Code != 200 || f.calls != 1 {
		t.Fatalf("allowed target: %d calls=%d", rec.Code, f.calls)
	}
}

func TestRender_ForwardsOnlyConfiguredHeaders(t *testing.T) {
	f := &fakeRenderer{healthy: true, res: render.Result{HTML: "x"}}
	h := newHandler(f, Options{ForwardHeaders: []string{"Authorization", "X-Api-Key"}})
	req := httptest.NewRequest(http.MethodGet, "/https://stg.example/", nil)
	req.Header.Set("Authorization", "Basic dGVzdDp0ZXN0cGFzcw==")
	req.Header.Set("Cookie", "session=1")
	h.ServeHTTP(httptest.NewRecorder(), req)
	got := f.reqs[0].Headers
	if len(got) != 1 || got["Authorization"] != "Basic dGVzdDp0ZXN0cGFzcw==" {
		t.Fatalf("headers: %v", got)
	}
	get(h, "/https://stg.example/")
	if f.reqs[1].Headers != nil {
		t.Fatalf("no incoming header means nothing forwarded: %v", f.reqs[1].Headers)
	}
}

func TestRender_RequestsWithDifferentCredentialsNeverShareARender(t *testing.T) {
	f := &fakeRenderer{healthy: true, res: render.Result{HTML: "x"}, delay: 100 * time.Millisecond}
	h := New(f, flow.New(4), Options{QueueTimeout: time.Second, ForwardHeaders: []string{"Authorization"}}, nil)
	var wg sync.WaitGroup
	for _, auth := range []string{"Basic a", "Basic a", "Basic b", ""} {
		wg.Go(func() {
			req := httptest.NewRequest(http.MethodGet, "/render?url=https://stg.example/", nil)
			if auth != "" {
				req.Header.Set("Authorization", auth)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)
		})
		time.Sleep(10 * time.Millisecond)
	}
	wg.Wait()
	if f.calls != 3 {
		t.Fatalf("want 3 renders (a shared, b, none), got %d", f.calls)
	}
}

func TestFlightKey(t *testing.T) {
	base := render.Request{URL: "https://stg.example/"}
	if flightKey(base) != base.URL {
		t.Fatal("no headers: the key is the URL")
	}
	a := render.Request{URL: base.URL, Headers: map[string]string{"Authorization": "Basic a", "X-Api-Key": "k"}}
	b := render.Request{URL: base.URL, Headers: map[string]string{"X-Api-Key": "k", "Authorization": "Basic a"}}
	c := render.Request{URL: base.URL, Headers: map[string]string{"Authorization": "Basic b", "X-Api-Key": "k"}}
	if flightKey(a) != flightKey(b) || flightKey(a) == flightKey(c) || strings.Contains(flightKey(a), "Basic") {
		t.Fatal("key must be stable, credential-specific and must not contain the credentials")
	}
}

func TestRender_ResponseHeadersOnEveryRenderResponse(t *testing.T) {
	extra := http.Header{"X-Robots-Tag": {"noindex"}}
	ok := &fakeRenderer{healthy: true, res: render.Result{HTML: "x"}}
	fail := &fakeRenderer{healthy: true, err: errors.New("boom")}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"200":     get(newHandler(ok, Options{ResponseHeaders: extra}), "/render?url=https://stg.example/"),
		"400":     get(newHandler(ok, Options{ResponseHeaders: extra}), "/render?url=ftp://x"),
		"403":     get(newHandler(ok, Options{ResponseHeaders: extra, Guard: fakeGuard{netguard.ErrNotAllowed}}), "/render?url=https://x.example/"),
		"502":     get(newHandler(fail, Options{ResponseHeaders: extra}), "/render?url=https://stg.example/"),
		"healthz": get(newHandler(ok, Options{ResponseHeaders: extra}), "/healthz"),
	} {
		want := "noindex"
		if name == "healthz" {
			want = ""
		}
		if got := rec.Header().Get("X-Robots-Tag"); got != want {
			t.Errorf("%s: X-Robots-Tag=%q, want %q", name, got, want)
		}
	}
}

func TestStripQuery_KeepsOrderAndEncodingOfRemainingParams(t *testing.T) {
	patterns := []string{"utm_*", "fbclid", "GCLID"}
	cases := map[string]string{
		"page=2&utm_source=ig&q=a%20b&fbclid=x&utm_medium=cpc": "page=2&q=a%20b",
		"UTM_Source=ig&gclid=1":                                "",
		"q=1&utm=keep&utm_=drop&fbclid2=keep":                  "q=1&utm=keep&fbclid2=keep",
		"a=1&&b&%zz=1&utm_source":                              "a=1&&b&%zz=1",
		"":                                                     "",
	}
	for in, want := range cases {
		u := &url.URL{RawQuery: in}
		stripQuery(u, patterns)
		if u.RawQuery != want {
			t.Errorf("%q: want %q, got %q", in, want, u.RawQuery)
		}
	}
	u := &url.URL{RawQuery: "utm_source=x", ForceQuery: true}
	stripQuery(u, patterns)
	if u.String() != "" || u.ForceQuery {
		t.Fatalf("an emptied query must not leave a trailing '?': %q", u.String())
	}
}

func TestRender_StrippedParamsShareOneRenderOfTheCleanURL(t *testing.T) {
	f := &fakeRenderer{healthy: true, res: render.Result{HTML: "x"}, delay: 100 * time.Millisecond}
	h := New(f, flow.New(4), Options{QueueTimeout: time.Second, StripQueryParams: []string{"utm_*", "fbclid"}}, nil)
	var wg sync.WaitGroup
	for _, q := range []string{"?utm_source=ig&id=1", "?id=1&fbclid=abc", "?id=1"} {
		wg.Go(func() { get(h, "/https://shop.example/items"+q) })
		time.Sleep(10 * time.Millisecond)
	}
	wg.Wait()
	if f.calls != 1 || f.reqs[0].URL != "https://shop.example/items?id=1" {
		t.Fatalf("want one render of the clean URL, got calls=%d reqs=%v", f.calls, f.reqs)
	}
}

func TestRender_CacheServesRepeated200WithAge(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := cache.New[render.Result](time.Minute, 10, func() time.Time { return now })
	f := &fakeRenderer{healthy: true, res: render.Result{HTML: "<html>ok</html>", StatusCode: 200, Trigger: render.TriggerReady}}
	h := newHandler(f, Options{Cache: c, CacheControl: "public, max-age=60", StripQueryParams: []string{"utm_*"}})

	first := get(h, "/render?url=https://shop.example/?utm_source=a")
	if first.Header().Get("X-Kagemusha-Cache") != "miss" || first.Header().Get("Age") != "" {
		t.Fatalf("first: %v", first.Header())
	}
	now = now.Add(7 * time.Second)
	hit := get(h, "/https://shop.example/?utm_source=b")
	if f.calls != 1 || hit.Code != 200 || hit.Body.String() != "<html>ok</html>" {
		t.Fatalf("second request must be served from cache: calls=%d %d", f.calls, hit.Code)
	}
	if hit.Header().Get("X-Kagemusha-Cache") != "hit" || hit.Header().Get("Age") != "7" ||
		hit.Header().Get("Cache-Control") != "public, max-age=60" || hit.Header().Get("X-Kagemusha-Trigger") != "ready" {
		t.Fatalf("hit headers: %v", hit.Header())
	}
}

func TestRender_CacheStoresOnly200(t *testing.T) {
	c := cache.New[render.Result](time.Minute, 10, nil)
	for _, res := range []render.Result{{HTML: "nf", StatusCode: 404}, {HTML: "auth", StatusCode: 401}} {
		f := &fakeRenderer{healthy: true, res: res}
		h := newHandler(f, Options{Cache: c})
		get(h, "/render?url=https://shop.example/x")
		get(h, "/render?url=https://shop.example/x")
		if f.calls != 2 {
			t.Fatalf("status %d must not be cached: calls=%d", res.StatusCode, f.calls)
		}
	}
	f := &fakeRenderer{healthy: true, err: errors.New("boom")}
	h := newHandler(f, Options{Cache: c})
	get(h, "/render?url=https://shop.example/y")
	get(h, "/render?url=https://shop.example/y")
	if f.calls != 2 || c.Len() != 0 {
		t.Fatalf("failures must not be cached: calls=%d len=%d", f.calls, c.Len())
	}
}

func TestRender_CachedPageIsServedOnlyToTheSameCredentials(t *testing.T) {
	c := cache.New[render.Result](time.Minute, 10, nil)
	f := &fakeRenderer{healthy: true, res: render.Result{HTML: "secret", StatusCode: 200}}
	h := newHandler(f, Options{Cache: c, ForwardHeaders: []string{"Authorization"}})
	do := func(auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/render?url=https://stg.example/", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	do("Basic good")
	if rec := do("Basic good"); rec.Header().Get("X-Kagemusha-Cache") != "hit" {
		t.Fatal("same credentials must hit")
	}
	for _, auth := range []string{"", "Basic other"} {
		if rec := do(auth); rec.Header().Get("X-Kagemusha-Cache") != "miss" {
			t.Fatalf("%q must never get the cached page", auth)
		}
	}
}

func TestRender_NoCacheHeaderWhenCacheIsOff(t *testing.T) {
	f := &fakeRenderer{healthy: true, res: render.Result{HTML: "x"}}
	if rec := get(newHandler(f, Options{}), "/render?url=https://shop.example/"); rec.Header().Get("X-Kagemusha-Cache") != "" {
		t.Fatalf("cache header must be absent when off: %v", rec.Header())
	}
}
