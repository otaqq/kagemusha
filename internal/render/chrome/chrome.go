// Package chrome implements render.Engine by driving headless Chrome (Chromium) with chromedp (CDP).
// It uses only navigation (Page), interception (Fetch), completion-signal evaluation (Runtime) and HTML capture (DOM),
// plus Browser.getVersion for health checks.
package chrome

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/otaqq/kagemusha/internal/render"
)

// ErrNotLaunched is returned when Render or Healthy is called before Launch.
var ErrNotLaunched = errors.New("chrome: engine not launched")

// Engine is one resident Chromium process.
type Engine struct {
	path string
	args []string
	log  *slog.Logger

	mu            sync.RWMutex
	browserCtx    context.Context
	allocCancel   context.CancelFunc
	browserCancel context.CancelFunc
	userAgent     string // browser default user agent plus the kagemusha marker
}

// uaMarker is appended to the browser user agent so edges can tell kagemusha's own requests from bots (loop prevention).
const uaMarker = " kagemusha"

// New creates an Engine from a launch definition without starting it.
func New(path string, args []string, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{path: path, args: args, log: log}
}

// Launch starts Chromium as a child process and connects over CDP. It honors the ctx deadline.
func (e *Engine) Launch(ctx context.Context) error {
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(e.path))
	opts = append(opts, flagsFromArgs(e.args)...)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(func(string, ...any) {}))

	done := make(chan error, 1)
	go func() { done <- chromedp.Do(browserCtx) }()
	select {
	case err := <-done:
		if err != nil {
			browserCancel()
			allocCancel()
			return fmt.Errorf("chrome: launch: %w", err)
		}
	case <-ctx.Done():
		browserCancel()
		allocCancel()
		return fmt.Errorf("chrome: launch: %w", ctx.Err())
	}
	ua := ""
	if v, err := chromedp.CallBrowser(browserCtx, browser.GetVersion, cdp.Empty{}); err == nil && v.UserAgent != "" {
		ua = v.UserAgent + uaMarker
	}
	e.mu.Lock()
	e.browserCtx, e.allocCancel, e.browserCancel, e.userAgent = browserCtx, allocCancel, browserCancel, ua
	e.mu.Unlock()
	return nil
}

// flagsFromArgs converts "--name=value" / "--name" arguments into chromedp flags.
// remote-debugging-port is ignored because chromedp sets it itself.
func flagsFromArgs(args []string) []chromedp.ExecAllocatorOption {
	var opts []chromedp.ExecAllocatorOption
	for _, a := range args {
		a = strings.TrimPrefix(a, "--")
		name, value, hasValue := strings.Cut(a, "=")
		if name == "" || name == "remote-debugging-port" {
			continue
		}
		if hasValue {
			opts = append(opts, chromedp.Flag(name, value))
		} else {
			opts = append(opts, chromedp.Flag(name, true))
		}
	}
	return opts
}

func (e *Engine) browser() (context.Context, error) {
	ctx, _, err := e.browserAndUA()
	return ctx, err
}

func (e *Engine) browserAndUA() (context.Context, string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.browserCtx == nil {
		return nil, "", ErrNotLaunched
	}
	return e.browserCtx, e.userAgent, nil
}

// Healthy reports whether the browser answers CDP.
func (e *Engine) Healthy(ctx context.Context) bool {
	bctx, err := e.browser()
	if err != nil {
		return false
	}
	tctx, cancel := context.WithTimeout(bctx, 3*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	_, err = chromedp.CallBrowser(tctx, browser.GetVersion, cdp.Empty{})
	return err == nil
}

// Close stops the Chromium process.
func (e *Engine) Close(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.browserCancel != nil {
		e.browserCancel()
		e.browserCancel = nil
	}
	if e.allocCancel != nil {
		e.allocCancel()
		e.allocCancel = nil
	}
	e.browserCtx = nil
	return nil
}

// activity tracks network activity during a render (for idle detection).
type activity struct {
	mu       sync.Mutex
	inflight int
	last     time.Time
	loaded   bool
}

func (a *activity) start() {
	a.mu.Lock()
	a.inflight++
	a.last = time.Now()
	a.mu.Unlock()
}

func (a *activity) finish() {
	a.mu.Lock()
	if a.inflight > 0 {
		a.inflight--
	}
	a.last = time.Now()
	a.mu.Unlock()
}

func (a *activity) load() {
	a.mu.Lock()
	a.loaded = true
	a.last = time.Now()
	a.mu.Unlock()
}

// idle reports true once load fired, no requests are in flight, and it has been quiet for at least quiet.
func (a *activity) idle(quiet time.Duration) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.loaded && a.inflight == 0 && time.Since(a.last) >= quiet
}

// Render renders the page in a disposable per-render BrowserContext and returns the HTML.
func (e *Engine) Render(ctx context.Context, req render.Request) (render.Result, error) {
	bctx, defaultUA, err := e.browserAndUA()
	if err != nil {
		return render.Result{}, err
	}
	start := time.Now()
	timeout := req.PageTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	// The tab is a child of browserCtx (cancelling its parent would take the whole browser down),
	// and cancelling the tab disposes its BrowserContext.
	// The absolute limit (deadlineCtx) is not the tab's parent: on timeout the tab stays alive
	// and only await stops, so the partial result can still be captured. Caller ctx cancellation is wired to the tab's cancel via AfterFunc
	tabCtx, tabCancel := chromedp.NewContext(bctx, chromedp.WithNewBrowserContext())
	defer tabCancel()
	stop := context.AfterFunc(ctx, tabCancel)
	defer stop()
	if err := chromedp.Do(tabCtx); err != nil {
		return render.Result{}, fmt.Errorf("chrome: create target: %w", err)
	}
	deadlineCtx, deadlineCancel := context.WithTimeout(tabCtx, timeout)
	defer deadlineCancel()

	act := &activity{last: time.Now()}
	filter := newFilter(req)
	if filter.active() {
		paused := chromedp.Events(tabCtx, fetch.RequestPaused)
		go func() {
			for ev, err := range paused {
				if err != nil {
					return
				}
				go e.decide(tabCtx, ev, filter)
			}
		}()
	}
	watch(tabCtx, network.RequestWillBeSent, act.start)
	watch(tabCtx, network.LoadingFinished, act.finish)
	watch(tabCtx, network.LoadingFailed, act.finish)
	watch(tabCtx, page.LoadEventFired, act.load)
	// In Chrome the main frame id equals the target id. The last main-frame document response is the page's HTTP status
	mainFrame := cdp.FrameID(chromedp.FromContext(tabCtx).Target.TargetID)
	var docStatus atomic.Int64
	responses := chromedp.Events(tabCtx, network.ResponseReceived)
	go func() {
		for ev, err := range responses {
			if err != nil {
				return
			}
			if ev.Type == network.ResourceTypeDocument && ev.FrameID == mainFrame && ev.Response != nil {
				docStatus.Store(ev.Response.Status)
			}
		}
	}()

	ua := req.UserAgent
	if ua == "" {
		ua = defaultUA
	}
	if ua != "" {
		if _, err := chromedp.Call(tabCtx, emulation.SetUserAgentOverride, emulation.SetUserAgentOverrideParams{UserAgent: ua}); err != nil {
			return render.Result{}, fmt.Errorf("chrome: set user agent: %w", err)
		}
	}

	if _, err := chromedp.Call(tabCtx, network.Enable, network.EnableParams{}); err != nil {
		return render.Result{}, fmt.Errorf("chrome: enable network: %w", err)
	}
	if filter.active() {
		if _, err := chromedp.Call(tabCtx, fetch.Enable, fetch.EnableParams{}); err != nil {
			return render.Result{}, fmt.Errorf("chrome: enable fetch: %w", err)
		}
	}
	if err := chromedp.Do(tabCtx, chromedp.Navigate(req.URL)); err != nil {
		return render.Result{}, fmt.Errorf("chrome: navigate: %w", err)
	}

	trigger := e.await(deadlineCtx, req, act)

	// Capture uses a short grace period independent of the deadline (so a timeout still returns the partial result)
	gctx, gcancel := context.WithTimeout(tabCtx, 5*time.Second)
	defer gcancel()
	res := render.Result{Trigger: trigger}
	declared, err := chromedp.Run(gctx, chromedp.Evaluate[int](statusScript))
	if err != nil {
		return render.Result{}, fmt.Errorf("chrome: capture: %w", err)
	}
	if res.HTML, err = chromedp.Run(gctx, chromedp.OuterHTML(chromedp.CSS("html"))); err != nil {
		return render.Result{}, fmt.Errorf("chrome: capture: %w", err)
	}
	res.StatusCode = statusCode(int(docStatus.Load()), declared)
	res.Duration = time.Since(start)
	return res, nil
}

// watch calls fn for every ev on the tab until the tab's context ends.
func watch[E any](tabCtx context.Context, ev cdp.Event[E], fn func()) {
	events := chromedp.Events(tabCtx, ev)
	go func() {
		for _, err := range events {
			if err != nil {
				return
			}
			fn()
		}
	}()
}

const readyScript = `!!window.prerenderReady`
const statusScript = `(function(){var m=document.querySelector('meta[name="prerender-status-code"]');return m?(parseInt(m.getAttribute('content'),10)||0):0})()`

// await is the completion loop: ready (first-class) → idle (fallback) → timeout (partial result).
func (e *Engine) await(deadlineCtx context.Context, req render.Request, act *activity) render.Trigger {
	quiet := req.IdleQuiet
	if quiet <= 0 {
		quiet = 3 * time.Second
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadlineCtx.Done():
			return render.TriggerTimeout
		case <-ticker.C:
		}
		if req.ReadyFlag {
			if ready, err := chromedp.Run(deadlineCtx, chromedp.Evaluate[bool](readyScript)); err == nil && ready {
				return render.TriggerReady
			}
		}
		if act.idle(quiet) {
			return render.TriggerIdle
		}
	}
}

// statusCode picks the response status: a non-200 HTTP status of the main document wins
// (an error page must never be served as a cacheable 200); otherwise the declared meta status, else 200.
func statusCode(httpStatus, declared int) int {
	if httpStatus != 0 && httpStatus != 200 {
		return httpStatus
	}
	if declared != 0 {
		return declared
	}
	return 200
}

// decide continues or fails a request paused by Fetch, according to the filter.
func (e *Engine) decide(tabCtx context.Context, ev fetch.EventRequestPaused, f *filter) {
	if f.blocked(tabCtx, ev.Request.URL) {
		_, _ = chromedp.Call(tabCtx, fetch.FailRequest, fetch.FailRequestParams{RequestID: ev.RequestID, ErrorReason: network.ErrorReasonBlockedByClient})
		return
	}
	_, _ = chromedp.Call(tabCtx, fetch.ContinueRequest, fetch.ContinueRequestParams{
		RequestID: ev.RequestID,
		Headers:   f.requestHeaders(ev.Request.URL, ev.Request.Headers),
	})
}
