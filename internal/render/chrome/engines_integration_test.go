//go:build integration

package chrome

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/otaqq/kagemusha/internal/render"
)

// engines launches every engine available in the environment:
// Chromium via KAGEMUSHA_ENGINE_PATH, Lightpanda via KAGEMUSHA_LIGHTPANDA_PATH.
func engines(t *testing.T) map[string]*Engine {
	t.Helper()
	out := map[string]*Engine{}
	if p := os.Getenv("KAGEMUSHA_ENGINE_PATH"); p != "" {
		out["chrome"] = New(p, []string{"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage"}, nil)
	}
	if p := os.Getenv("KAGEMUSHA_LIGHTPANDA_PATH"); p != "" {
		out["lightpanda"] = NewLightpanda(p, []string{"serve"}, nil)
	}
	if len(out) == 0 {
		t.Skip("no engine path set")
	}
	for name, e := range out {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := e.Launch(ctx); err != nil {
			cancel()
			t.Fatalf("%s: %v", name, err)
		}
		cancel()
		t.Cleanup(func() { _ = e.Close(context.Background()) })
	}
	return out
}

// The contract every engine must meet: the same scenarios as the Chrome integration tests.
func TestEngines_Contract(t *testing.T) {
	for name, e := range engines(t) {
		t.Run(name, func(t *testing.T) {
			if !e.Healthy(context.Background()) {
				t.Fatal("unhealthy after launch")
			}
			srv := spaServer(t)
			t.Run("prerenderReady", func(t *testing.T) {
				res, err := e.Render(context.Background(), render.Request{URL: srv.URL + "/", ReadyFlag: true, IdleQuiet: 3 * time.Second, PageTimeout: 20 * time.Second})
				if err != nil || res.Trigger != render.TriggerReady || res.StatusCode != 200 || !strings.Contains(res.HTML, "rendered title") {
					t.Fatalf("err=%v trigger=%s status=%d html=%s", err, res.Trigger, res.StatusCode, res.HTML)
				}
			})
			t.Run("idle", func(t *testing.T) {
				res, err := e.Render(context.Background(), render.Request{URL: srv.URL + "/", IdleQuiet: 500 * time.Millisecond, PageTimeout: 20 * time.Second})
				if err != nil || res.Trigger != render.TriggerIdle || !strings.Contains(res.HTML, "rendered title") {
					t.Fatalf("err=%v trigger=%s html=%s", err, res.Trigger, res.HTML)
				}
			})
			t.Run("timeout returns partial result", func(t *testing.T) {
				res, err := e.Render(context.Background(), render.Request{URL: srv.URL + "/never.html", ReadyFlag: true, IdleQuiet: time.Hour, PageTimeout: 2 * time.Second})
				if err != nil || res.Trigger != render.TriggerTimeout || !strings.Contains(res.HTML, "never-ready") {
					t.Fatalf("err=%v trigger=%s html=%s", err, res.Trigger, res.HTML)
				}
			})
			t.Run("declared status and domain blocking", func(t *testing.T) {
				res, err := e.Render(context.Background(), render.Request{URL: srv.URL + "/notfound.html", ReadyFlag: true, IdleQuiet: 500 * time.Millisecond, PageTimeout: 20 * time.Second,
					BlockDomains: []string{"blocked.invalid"}})
				if err != nil || res.StatusCode != 404 || !strings.Contains(res.HTML, "beacon:blocked") {
					t.Fatalf("err=%v status=%d html=%s", err, res.StatusCode, res.HTML)
				}
			})
			t.Run("HTTP error status", func(t *testing.T) {
				res, err := e.Render(context.Background(), render.Request{URL: srv.URL + "/status/401", ReadyFlag: true, IdleQuiet: 300 * time.Millisecond, PageTimeout: 10 * time.Second})
				if err != nil || res.StatusCode != 401 {
					t.Fatalf("err=%v status=%d", err, res.StatusCode)
				}
			})
			t.Run("forwarded auth, guard, user agent marker", func(t *testing.T) {
				site, _, rec := basicAuthSite(t)
				auth := "Basic dGVzdDp0ZXN0cGFzcw=="
				res, err := e.Render(context.Background(), render.Request{URL: site.URL + "/", ReadyFlag: true, IdleQuiet: time.Second, PageTimeout: 15 * time.Second,
					Headers: map[string]string{"Authorization": auth}, Guard: denyHost("localhost")})
				if err != nil || res.StatusCode != 200 || !strings.Contains(res.HTML, "secret-content|probe-blocked") {
					t.Fatalf("err=%v status=%d html=%s", err, res.StatusCode, res.HTML)
				}
				if rec.get("/probe") != nil {
					t.Fatal("guard-denied host was reached")
				}
				if ua := rec.get("/").Get("User-Agent"); !strings.Contains(ua, "kagemusha") {
					t.Fatalf("user agent marker missing: %q", ua)
				}
			})
			t.Run("cross-host redirect", func(t *testing.T) {
				alias, canonical := aliasSite(t)
				for _, path := range []string{"/spa", "/http"} {
					res, err := e.Render(context.Background(), render.Request{URL: alias + path, ReadyFlag: true, IdleQuiet: 2 * time.Second, PageTimeout: 10 * time.Second, RedirectCrossHost: true})
					if err != nil || res.StatusCode != 302 || res.Location != canonical {
						t.Fatalf("%s: err=%v status=%d location=%s", path, err, res.StatusCode, res.Location)
					}
				}
			})
			t.Run("strip scripts", func(t *testing.T) {
				res, err := e.Render(context.Background(), render.Request{URL: scriptsServer(t), ReadyFlag: true, IdleQuiet: time.Second, PageTimeout: 10 * time.Second, StripScripts: true})
				if err != nil || !strings.Contains(res.HTML, "application/ld+json") || strings.Contains(res.HTML, "/app.js") {
					t.Fatalf("err=%v html=%s", err, res.HTML)
				}
			})
		})
	}
}

var spaceRE = regexp.MustCompile(`\s+`)

// visibleText reduces HTML to its text, so engines are compared on content, not serialization details.
func visibleText(html string) string {
	html = regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>|<[^>]+>`).ReplaceAllString(html, " ")
	return strings.TrimSpace(spaceRE.ReplaceAllString(html, " "))
}

// The graduation test for Lightpanda: the same SPA renders to the same content on every engine.
func TestEngines_RenderTheSameContent(t *testing.T) {
	all := engines(t)
	if len(all) < 2 {
		t.Skip("needs both engines")
	}
	srv := spaServer(t)
	texts := map[string]string{}
	for name, e := range all {
		res, err := e.Render(context.Background(), render.Request{URL: srv.URL + "/", ReadyFlag: true, IdleQuiet: 3 * time.Second, PageTimeout: 20 * time.Second})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		texts[name] = visibleText(res.HTML)
	}
	if texts["chrome"] != texts["lightpanda"] {
		t.Fatalf("content differs:\nchrome:     %q\nlightpanda: %q", texts["chrome"], texts["lightpanda"])
	}
}

// Renders run concurrently (MAX_CONCURRENT): Lightpanda gets one CDP connection per render.
func TestEngines_ConcurrentRenders(t *testing.T) {
	for name, e := range engines(t) {
		t.Run(name, func(t *testing.T) {
			srv := spaServer(t)
			errs := make(chan error, 8)
			for range 8 {
				go func() {
					res, err := e.Render(context.Background(), render.Request{URL: srv.URL + "/", ReadyFlag: true, IdleQuiet: 3 * time.Second, PageTimeout: 20 * time.Second})
					if err == nil && !strings.Contains(res.HTML, "rendered title") {
						err = fmt.Errorf("incomplete render: %s", res.HTML)
					}
					errs <- err
				}()
			}
			for range 8 {
				if err := <-errs; err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
