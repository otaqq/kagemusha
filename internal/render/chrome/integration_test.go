//go:build integration

package chrome

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	mux.HandleFunc("/status/{code}", func(w http.ResponseWriter, r *http.Request) {
		var code int
		_, _ = fmt.Sscan(r.PathValue("code"), &code)
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(code)
		_, _ = fmt.Fprintf(w, `<html><body><div id="app">error page %d</div></body></html>`, code)
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

func TestIntegration_HTTPErrorStatusOfTheDocumentIsReturned(t *testing.T) {
	e := launch(t)
	srv := spaServer(t)
	for _, code := range []int{401, 404, 500} {
		res, err := e.Render(context.Background(), render.Request{URL: fmt.Sprintf("%s/status/%d", srv.URL, code), ReadyFlag: true, IdleQuiet: 300 * time.Millisecond, PageTimeout: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != code || !strings.Contains(res.HTML, fmt.Sprintf("error page %d", code)) {
			t.Fatalf("want %d, got status=%d html=%s", code, res.StatusCode, res.HTML)
		}
	}
}

// recorder captures the headers each request arrived with.
type recorder struct {
	mu   sync.Mutex
	seen map[string]http.Header // path → headers
}

func (r *recorder) record(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen[req.URL.Path] = req.Header.Clone()
}

func (r *recorder) get(path string) http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen[path]
}

// basicAuthSite is a Basic-auth staging site whose page calls its own API and a third-party host.
func basicAuthSite(t *testing.T) (site, third *httptest.Server, rec *recorder) {
	t.Helper()
	rec = &recorder{seen: map[string]http.Header{}}
	third = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = w.Write([]byte("probe-ok"))
	}))
	t.Cleanup(third.Close)
	// The page is on 127.0.0.1 and the third party on localhost: same machine, different origins
	thirdURL := strings.Replace(third.URL, "127.0.0.1", "localhost", 1)
	site = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		if u, p, ok := r.BasicAuth(); !ok || u != "test" || p != "testpass" {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`<html><body>auth required</body></html>`))
			return
		}
		if r.URL.Path == "/api/secret" {
			_, _ = w.Write([]byte("secret-content"))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><body><div id="app">loading</div><script>
Promise.all([
  fetch('/api/secret').then(function (r) { return r.text(); }),
  fetch('%s/probe').then(function (r) { return r.text(); }).catch(function () { return 'probe-blocked'; })
]).then(function (v) { document.getElementById('app').textContent = v.join('|'); window.prerenderReady = true; });
</script></body></html>`, thirdURL)
	}))
	t.Cleanup(site.Close)
	return site, third, rec
}

func TestIntegration_ForwardedBasicAuthReachesOnlyTheTargetOrigin(t *testing.T) {
	e := launch(t)
	site, _, rec := basicAuthSite(t)
	auth := "Basic dGVzdDp0ZXN0cGFzcw==" // test:testpass
	res, err := e.Render(context.Background(), render.Request{URL: site.URL + "/", ReadyFlag: true, IdleQuiet: time.Second, PageTimeout: 15 * time.Second,
		Headers: map[string]string{"Authorization": auth}})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || !strings.Contains(res.HTML, "secret-content|probe-ok") {
		t.Fatalf("status=%d html=%s", res.StatusCode, res.HTML)
	}
	if rec.get("/api/secret").Get("Authorization") != auth {
		t.Fatal("same-origin sub-request must carry the forwarded header")
	}
	probe := rec.get("/probe")
	if probe == nil || probe.Get("Authorization") != "" {
		t.Fatalf("third-party host must not receive the forwarded header: %v", probe)
	}
	if ua := probe.Get("User-Agent"); !strings.HasSuffix(ua, " kagemusha") {
		t.Fatalf("user agent must carry the kagemusha marker: %q", ua)
	}
}

func TestIntegration_WithoutCredentialsTheAuthWallIsReturnedAs401(t *testing.T) {
	e := launch(t)
	site, _, _ := basicAuthSite(t)
	res, err := e.Render(context.Background(), render.Request{URL: site.URL + "/", ReadyFlag: true, IdleQuiet: 300 * time.Millisecond, PageTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 401 || strings.Contains(res.HTML, "secret-content") {
		t.Fatalf("status=%d html=%s", res.StatusCode, res.HTML)
	}
}

type denyHost string

func (d denyHost) CheckHost(_ context.Context, host string) error {
	if host == string(d) {
		return errors.New("denied")
	}
	return nil
}

func TestIntegration_GuardBlocksSubRequestsAndUserAgentOverride(t *testing.T) {
	e := launch(t)
	site, _, rec := basicAuthSite(t)
	res, err := e.Render(context.Background(), render.Request{URL: site.URL + "/", ReadyFlag: true, IdleQuiet: time.Second, PageTimeout: 15 * time.Second,
		Headers: map[string]string{"Authorization": "Basic dGVzdDp0ZXN0cGFzcw=="}, Guard: denyHost("localhost"), UserAgent: "CustomRenderer/1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.HTML, "secret-content|probe-blocked") || rec.get("/probe") != nil {
		t.Fatalf("guard-denied host must never be reached: html=%s", res.HTML)
	}
	if ua := rec.get("/").Get("User-Agent"); ua != "CustomRenderer/1.0" {
		t.Fatalf("user agent override: %q", ua)
	}
}

func TestIntegration_BlockedResourceTypesAreNeverFetchedButStayInHTML(t *testing.T) {
	e := launch(t)
	rec := &recorder{seen: map[string]http.Header{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><head><style>@font-face{font-family:F;src:url(/f.woff2)}body{font-family:F}</style></head>
<body><img src="/photo.png" alt="item photo"><div id="app">text</div><script>window.prerenderReady = true;</script></body></html>`))
		case "/photo.png":
			w.Header().Set("Content-Type", "image/png")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	res, err := e.Render(context.Background(), render.Request{URL: srv.URL + "/", ReadyFlag: false, IdleQuiet: 500 * time.Millisecond, PageTimeout: 10 * time.Second,
		BlockResourceTypes: []string{"Image", "Font"}})
	if err != nil {
		t.Fatal(err)
	}
	if rec.get("/photo.png") != nil || rec.get("/f.woff2") != nil {
		t.Fatal("blocked resource types must never reach the server")
	}
	if res.StatusCode != 200 || !strings.Contains(res.HTML, `<img src="/photo.png" alt="item photo">`) {
		t.Fatalf("the tag must stay in the HTML: status=%d html=%s", res.StatusCode, res.HTML)
	}
}

// aliasSite serves an alias page on 127.0.0.1 that moves to the canonical host (localhost),
// either by SPA navigation (/spa) or by an HTTP redirect (/http).
func aliasSite(t *testing.T) (alias, canonical string) {
	t.Helper()
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><div id="app">canonical page</div><script>window.prerenderReady = true;</script></body></html>`))
	}))
	t.Cleanup(dest.Close)
	canonical = strings.Replace(dest.URL, "127.0.0.1", "localhost", 1) + "/landing"
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/http":
			http.Redirect(w, r, canonical, http.StatusMovedPermanently)
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprintf(w, `<html><body><div id="app">alias</div><script>
window.addEventListener('load', function () { setTimeout(function () { location.href = '%s'; }, 100); });
</script></body></html>`, canonical)
		}
	}))
	t.Cleanup(src.Close)
	return src.URL, canonical
}

func TestIntegration_CrossHostNavigationRedirects(t *testing.T) {
	e := launch(t)
	alias, canonical := aliasSite(t)
	for _, path := range []string{"/spa", "/http"} {
		res, err := e.Render(context.Background(), render.Request{URL: alias + path, ReadyFlag: true, IdleQuiet: 2 * time.Second, PageTimeout: 10 * time.Second,
			RedirectCrossHost: true})
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if res.StatusCode != 302 || res.Location != canonical || res.Trigger != render.TriggerRedirect || res.HTML != "" {
			t.Fatalf("%s: status=%d location=%s trigger=%s html=%q", path, res.StatusCode, res.Location, res.Trigger, res.HTML)
		}
	}
}

func TestIntegration_CrossHostNavigationFollowsByDefault(t *testing.T) {
	e := launch(t)
	alias, _ := aliasSite(t)
	res, err := e.Render(context.Background(), render.Request{URL: alias + "/spa", ReadyFlag: true, IdleQuiet: 2 * time.Second, PageTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || res.Location != "" || !strings.Contains(res.HTML, "canonical page") {
		t.Fatalf("status=%d location=%s html=%s", res.StatusCode, res.Location, res.HTML)
	}
}

const scriptsPage = `<html><head>
<link rel="modulepreload" href="/m.js"><link rel="preload" as="script" href="/p.js"><link rel="preload" as="style" href="/s.css">
<script type="application/ld+json">{"@type":"Product","name":"tent"}</script>
<script type="application/json" id="state">{"items":1}</script>
<script src="/app.js"></script><script type="module">void 0</script><script type="text/javascript">void 0</script>
</head><body><div id="app">page</div><script>window.prerenderReady = true;</script></body></html>`

func scriptsServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			w.Header().Set("Content-Type", "text/javascript")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(scriptsPage))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/"
}

func TestIntegration_StripScriptsKeepsDataScripts(t *testing.T) {
	e := launch(t)
	res, err := e.Render(context.Background(), render.Request{URL: scriptsServer(t), ReadyFlag: true, IdleQuiet: time.Second, PageTimeout: 10 * time.Second, StripScripts: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{`application/ld+json`, `"@type":"Product"`, `id="state"`, `rel="preload" as="style"`, `<div id="app">page</div>`} {
		if !strings.Contains(res.HTML, keep) {
			t.Errorf("must keep %s: %s", keep, res.HTML)
		}
	}
	for _, drop := range []string{`/app.js`, `type="module"`, `text/javascript`, `prerenderReady`, `modulepreload`, `as="script"`} {
		if strings.Contains(res.HTML, drop) {
			t.Errorf("must drop %s: %s", drop, res.HTML)
		}
	}
}

func TestIntegration_ScriptsAreKeptByDefault(t *testing.T) {
	e := launch(t)
	res, err := e.Render(context.Background(), render.Request{URL: scriptsServer(t), ReadyFlag: true, IdleQuiet: time.Second, PageTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.HTML, `/app.js`) || !strings.Contains(res.HTML, `modulepreload`) {
		t.Fatalf("scripts must stay when StripScripts is off: %s", res.HTML)
	}
}
