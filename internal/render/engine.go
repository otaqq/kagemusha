// Package render defines the render engine abstraction.
// It uses only four CDP areas (navigation, interception, completion-signal evaluation, HTML capture)
// so Chrome and Lightpanda can be handled the same way.
package render

import (
	"context"
	"time"
)

// Trigger is what completed a render.
type Trigger string

const (
	TriggerReady   Trigger = "ready"   // window.prerenderReady === true
	TriggerIdle    Trigger = "idle"    // network-idle fallback
	TriggerTimeout Trigger = "timeout" // absolute limit reached (partial result)
)

// Request is the input of one render.
type Request struct {
	URL          string
	ReadyFlag    bool
	IdleQuiet    time.Duration
	PageTimeout  time.Duration
	BlockDomains []string
	AllowDomains []string
}

// Result is the output of one render. StatusCode is the value the page declared with
// <meta name="prerender-status-code"> (200 if not declared).
type Result struct {
	HTML       string
	StatusCode int
	Trigger    Trigger
	Duration   time.Duration
}

// Engine is one resident browser process. Implementations: Chrome (chromedp) and Lightpanda.
// Every method honors the ctx deadline and reports a dead browser as an error, never by killing the process.
type Engine interface {
	// Launch starts the engine process and connects over CDP.
	Launch(ctx context.Context) error
	// Render renders the page in a disposable per-render context and returns the HTML.
	Render(ctx context.Context, req Request) (Result, error)
	// Healthy reports whether the engine responds (used by /healthz).
	Healthy(ctx context.Context) bool
	// Close stops the engine process.
	Close(ctx context.Context) error
}

// Spec is the launch definition per engine type (capability differences live here).
type Spec struct {
	Name        string
	DefaultPath string
	Args        []string
	// HasLayout reports whether layout-based completion heuristics (visible text, etc.) are available.
	HasLayout bool
}

// Specs defines the bundled engines. KAGEMUSHA_ENGINE_PATH overrides DefaultPath.
var Specs = map[string]Spec{
	"chrome": {
		Name:        "chrome",
		DefaultPath: "/usr/bin/chromium",
		Args: []string{
			"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage",
			"--remote-debugging-port=0",
		},
		HasLayout: true,
	},
	"lightpanda": {
		Name:        "lightpanda",
		DefaultPath: "/usr/bin/lightpanda",
		Args:        []string{"serve", "--host", "127.0.0.1", "--port", "0"},
		HasLayout:   false,
	},
}
