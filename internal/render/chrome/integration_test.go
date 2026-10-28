//go:build integration

package chrome

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otaqq/kagemusha/internal/render"
)

// Real-browser integration tests. Run them with KAGEMUSHA_ENGINE_PATH pointing at Chromium:
//
//	go test -tags integration ./internal/render/chrome/
func launch(t *testing.T) *Engine {
	t.Helper()
	path := os.Getenv("KAGEMUSHA_ENGINE_PATH")
	if path == "" {
		t.Skip("KAGEMUSHA_ENGINE_PATH not set")
	}
	e := New(path, []string{"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := e.Launch(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	return e
}

func spaServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir, _ := filepath.Abs("../../../testdata/spa")
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(dir)))
	mux.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"title":"rendered title"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestIntegration_PrerenderReadyCompletesWithAPIContent(t *testing.T) {
	e := launch(t)
	srv := spaServer(t)
	if !e.Healthy(context.Background()) {
		t.Fatal("engine unhealthy after launch")
	}
	res, err := e.Render(context.Background(), render.Request{URL: srv.URL + "/", ReadyFlag: true, IdleQuiet: 3 * time.Second, PageTimeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.Trigger != render.TriggerReady || res.StatusCode != 200 || !strings.Contains(res.HTML, "rendered title") {
		t.Fatalf("trigger=%s status=%d html=%s", res.Trigger, res.StatusCode, res.HTML)
	}
}

func TestIntegration_ReadyFlagOffCompletesOnIdle(t *testing.T) {
	e := launch(t)
	srv := spaServer(t)
	res, err := e.Render(context.Background(), render.Request{URL: srv.URL + "/", ReadyFlag: false, IdleQuiet: 500 * time.Millisecond, PageTimeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.Trigger != render.TriggerIdle || !strings.Contains(res.HTML, "rendered title") {
		t.Fatalf("trigger=%s html=%s", res.Trigger, res.HTML)
	}
}

func TestIntegration_DeclaredStatusAndDomainBlocking(t *testing.T) {
	e := launch(t)
	srv := spaServer(t)
	res, err := e.Render(context.Background(), render.Request{URL: srv.URL + "/notfound.html", ReadyFlag: true, IdleQuiet: 500 * time.Millisecond, PageTimeout: 20 * time.Second,
		BlockDomains: []string{"blocked.invalid"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 404 || !strings.Contains(res.HTML, "beacon:blocked") {
		t.Fatalf("status=%d html=%s", res.StatusCode, res.HTML)
	}
}

func TestIntegration_TimeoutReturnsPartialResult(t *testing.T) {
	e := launch(t)
	srv := spaServer(t)
	res, err := e.Render(context.Background(), render.Request{URL: srv.URL + "/never.html", ReadyFlag: true, IdleQuiet: time.Hour, PageTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.Trigger != render.TriggerTimeout || !strings.Contains(res.HTML, "never-ready") {
		t.Fatalf("trigger=%s html=%s", res.Trigger, res.HTML)
	}
}
