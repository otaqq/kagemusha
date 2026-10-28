package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/otaqq/kagemusha/internal/flow"
	"github.com/otaqq/kagemusha/internal/render"
)

type fakeRenderer struct {
	healthy bool
	res     render.Result
	err     error
	delay   time.Duration
	calls   int
}

func (f *fakeRenderer) Render(ctx context.Context, req render.Request) (render.Result, error) {
	f.calls++
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
