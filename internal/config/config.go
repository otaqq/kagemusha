// Package config loads settings from the environment. Every setting has a safe default.
package config

import (
	"fmt"
	"net/http"
	"net/textproto"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Engine is the render engine type.
type Engine string

const (
	EngineChrome     Engine = "chrome"
	EngineLightpanda Engine = "lightpanda"
)

// Config holds all kagemusha settings.
type Config struct {
	Engine               Engine
	EnginePath           string
	EngineArgs           []string
	ListenHost           string
	Port                 int
	MaxConcurrent        int
	QueueTimeout         time.Duration
	ReadyFlag            bool
	IdleQuiet            time.Duration
	PageTimeout          time.Duration
	BlockDomains         []string
	AllowDomains         []string
	DenyPaths            []string
	CacheControl         string
	WorkerMaxRenders     int
	WorkerMaxAge         time.Duration
	BrowserRestartPeriod time.Duration
	WatchdogTimeout      time.Duration
	TargetHosts          []string
	AllowPrivateTargets  bool
	ForwardHeaders       []string
	ForwardHeadersHosts  []string
	ResponseHeaders      http.Header
	UserAgent            string
	StripQueryParams     []string
	CacheTTL             time.Duration
	CacheMaxEntries      int
	CacheMaxMB           int
	BlockResourceTypes   []string
	RedirectCrossHost    bool
	StripScripts         bool
	Metrics              bool
	StallTimeout         time.Duration
}

// Default returns the default settings.
func Default() Config {
	return Config{
		Engine:               EngineChrome,
		Port:                 3000,
		MaxConcurrent:        4,
		QueueTimeout:         20 * time.Second,
		ReadyFlag:            true,
		IdleQuiet:            3 * time.Second,
		PageTimeout:          30 * time.Second,
		CacheControl:         "no-store",
		WorkerMaxRenders:     1000,
		WorkerMaxAge:         6 * time.Hour,
		BrowserRestartPeriod: 10 * time.Minute,
		WatchdogTimeout:      10 * time.Second,
		CacheMaxEntries:      100,
		CacheMaxMB:           64,
		Metrics:              true,
		StallTimeout:         5 * time.Minute,
	}
}

// Getenv abstracts environment lookup (replaced in tests).
type Getenv func(string) string

// Load builds the Config from KAGEMUSHA_* environment variables.
func Load(getenv Getenv) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	c := Default()
	var err error
	if v := getenv("KAGEMUSHA_ENGINE"); v != "" {
		switch Engine(v) {
		case EngineChrome, EngineLightpanda:
			c.Engine = Engine(v)
		default:
			return c, fmt.Errorf("KAGEMUSHA_ENGINE: unknown engine %q", v)
		}
	}
	c.EnginePath = getenv("KAGEMUSHA_ENGINE_PATH")
	c.EngineArgs = strings.Fields(getenv("KAGEMUSHA_ENGINE_ARGS"))
	if len(c.EngineArgs) == 0 {
		c.EngineArgs = nil
	}
	c.ListenHost = getenv("KAGEMUSHA_LISTEN_HOST")
	if c.Port, err = intEnv(getenv, "KAGEMUSHA_PORT", c.Port); err != nil {
		return c, err
	}
	if c.MaxConcurrent, err = intEnv(getenv, "KAGEMUSHA_MAX_CONCURRENT", c.MaxConcurrent); err != nil {
		return c, err
	}
	if c.MaxConcurrent < 1 {
		return c, fmt.Errorf("KAGEMUSHA_MAX_CONCURRENT: must be >= 1")
	}
	if c.QueueTimeout, err = msEnv(getenv, "KAGEMUSHA_QUEUE_TIMEOUT_MS", c.QueueTimeout); err != nil {
		return c, err
	}
	if c.ReadyFlag, err = boolEnv(getenv, "KAGEMUSHA_READY_FLAG", c.ReadyFlag); err != nil {
		return c, err
	}
	if c.IdleQuiet, err = msEnv(getenv, "KAGEMUSHA_IDLE_QUIET_MS", c.IdleQuiet); err != nil {
		return c, err
	}
	if c.PageTimeout, err = msEnv(getenv, "KAGEMUSHA_PAGE_TIMEOUT_MS", c.PageTimeout); err != nil {
		return c, err
	}
	c.BlockDomains = listEnv(getenv, "KAGEMUSHA_BLOCK_DOMAINS")
	c.AllowDomains = listEnv(getenv, "KAGEMUSHA_ALLOW_DOMAINS")
	c.DenyPaths = listEnv(getenv, "KAGEMUSHA_DENY_PATHS")
	if v := getenv("KAGEMUSHA_CACHE_CONTROL"); v != "" {
		c.CacheControl = v
	}
	if c.WorkerMaxRenders, err = intEnv(getenv, "KAGEMUSHA_WORKER_MAX_RENDERS", c.WorkerMaxRenders); err != nil {
		return c, err
	}
	if c.WorkerMaxAge, err = durEnv(getenv, "KAGEMUSHA_WORKER_MAX_AGE", c.WorkerMaxAge); err != nil {
		return c, err
	}
	if c.BrowserRestartPeriod, err = durEnv(getenv, "KAGEMUSHA_BROWSER_RESTART_PERIOD", c.BrowserRestartPeriod); err != nil {
		return c, err
	}
	if c.WatchdogTimeout, err = msEnv(getenv, "KAGEMUSHA_WATCHDOG_TIMEOUT_MS", c.WatchdogTimeout); err != nil {
		return c, err
	}
	c.TargetHosts = listEnv(getenv, "KAGEMUSHA_TARGET_HOSTS")
	if c.AllowPrivateTargets, err = boolEnv(getenv, "KAGEMUSHA_ALLOW_PRIVATE_TARGETS", c.AllowPrivateTargets); err != nil {
		return c, err
	}
	if c.ForwardHeaders, err = forwardHeadersEnv(getenv, "KAGEMUSHA_FORWARD_HEADERS"); err != nil {
		return c, err
	}
	c.ForwardHeadersHosts = listEnv(getenv, "KAGEMUSHA_FORWARD_HEADERS_HOSTS")
	if c.ResponseHeaders, err = responseHeadersEnv(getenv, "KAGEMUSHA_RESPONSE_HEADERS"); err != nil {
		return c, err
	}
	c.UserAgent = getenv("KAGEMUSHA_USER_AGENT")
	if c.StripQueryParams, err = stripParamsEnv(getenv, "KAGEMUSHA_STRIP_QUERY_PARAMS"); err != nil {
		return c, err
	}
	if c.CacheTTL, err = durEnv(getenv, "KAGEMUSHA_CACHE_TTL", c.CacheTTL); err != nil {
		return c, err
	}
	if c.CacheTTL < 0 {
		return c, fmt.Errorf("KAGEMUSHA_CACHE_TTL: must be >= 0")
	}
	if c.CacheMaxEntries, err = intEnv(getenv, "KAGEMUSHA_CACHE_MAX_ENTRIES", c.CacheMaxEntries); err != nil {
		return c, err
	}
	if c.CacheMaxEntries < 1 {
		return c, fmt.Errorf("KAGEMUSHA_CACHE_MAX_ENTRIES: must be >= 1")
	}
	if c.CacheMaxMB, err = intEnv(getenv, "KAGEMUSHA_CACHE_MAX_MB", c.CacheMaxMB); err != nil {
		return c, err
	}
	if c.CacheMaxMB < 1 {
		return c, fmt.Errorf("KAGEMUSHA_CACHE_MAX_MB: must be >= 1")
	}
	if c.BlockResourceTypes, err = resourceTypesEnv(getenv, "KAGEMUSHA_BLOCK_RESOURCE_TYPES"); err != nil {
		return c, err
	}
	if c.StripScripts, err = boolEnv(getenv, "KAGEMUSHA_STRIP_SCRIPTS", c.StripScripts); err != nil {
		return c, err
	}
	if c.Metrics, err = boolEnv(getenv, "KAGEMUSHA_METRICS", c.Metrics); err != nil {
		return c, err
	}
	if c.StallTimeout, err = durEnv(getenv, "KAGEMUSHA_STALL_TIMEOUT", c.StallTimeout); err != nil {
		return c, err
	}
	if c.StallTimeout < 0 {
		return c, fmt.Errorf("KAGEMUSHA_STALL_TIMEOUT: must be >= 0")
	}
	switch v := strings.ToLower(getenv("KAGEMUSHA_CROSS_HOST_NAVIGATION")); v {
	case "", "follow":
	case "redirect":
		c.RedirectCrossHost = true
	default:
		return c, fmt.Errorf("KAGEMUSHA_CROSS_HOST_NAVIGATION: want follow or redirect, got %q", v)
	}
	return c, nil
}

// resourceTypes are the CDP Network.ResourceType names that may be blocked (Document would block the page itself).
var resourceTypes = []string{
	"Stylesheet", "Image", "Media", "Font", "Script", "TextTrack", "XHR", "Fetch", "Prefetch", "EventSource",
	"WebSocket", "Manifest", "SignedExchange", "Ping", "CSPViolationReport", "Preflight", "FedCM", "Other",
}

// resourceTypesEnv parses resource type names case-insensitively into their CDP spelling.
func resourceTypesEnv(getenv Getenv, key string) ([]string, error) {
	var out []string
	for _, v := range listEnv(getenv, key) {
		i := slices.IndexFunc(resourceTypes, func(t string) bool { return strings.EqualFold(t, v) })
		if i < 0 {
			return nil, fmt.Errorf("%s: unknown or unblockable resource type %q", key, v)
		}
		out = append(out, resourceTypes[i])
	}
	return out, nil
}

// stripParamsEnv parses query parameter names; a trailing "*" matches any suffix (utm_*).
func stripParamsEnv(getenv Getenv, key string) ([]string, error) {
	list := listEnv(getenv, key)
	for _, p := range list {
		name := strings.TrimSuffix(p, "*")
		if name == "" || strings.Contains(name, "*") {
			return nil, fmt.Errorf("%s: invalid pattern %q (a name, optionally ending with *)", key, p)
		}
	}
	return list, nil
}

var tokenRE = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")

// unforwardable are headers that describe the connection or the message itself, not the client.
var unforwardable = map[string]bool{
	"Host": true, "Connection": true, "Content-Length": true, "Transfer-Encoding": true, "Te": true,
	"Trailer": true, "Upgrade": true, "Keep-Alive": true, "Proxy-Authorization": true, "Proxy-Connection": true,
}

func forwardHeadersEnv(getenv Getenv, key string) ([]string, error) {
	var out []string
	for _, n := range listEnv(getenv, key) {
		if !tokenRE.MatchString(n) {
			return nil, fmt.Errorf("%s: invalid header name %q", key, n)
		}
		n = textproto.CanonicalMIMEHeaderKey(n)
		if unforwardable[n] {
			return nil, fmt.Errorf("%s: %s cannot be forwarded", key, n)
		}
		out = append(out, n)
	}
	return out, nil
}

// reserved are response headers owned by the response contract.
var reserved = map[string]bool{
	"Cache-Control": true, "Content-Type": true, "Content-Length": true, "Location": true, "Age": true,
	"X-Kagemusha-Trigger": true, "X-Kagemusha-Cache": true,
}

// responseHeadersEnv parses newline-separated "Name: value" lines (values may contain commas).
func responseHeadersEnv(getenv Getenv, key string) (http.Header, error) {
	v := getenv(key)
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	h := http.Header{}
	for _, line := range strings.Split(v, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || !tokenRE.MatchString(name) || value == "" {
			return nil, fmt.Errorf("%s: invalid header line %q (want \"Name: value\")", key, line)
		}
		if reserved[textproto.CanonicalMIMEHeaderKey(name)] {
			return nil, fmt.Errorf("%s: %s is set by kagemusha and cannot be overridden", key, name)
		}
		h.Add(name, value)
	}
	return h, nil
}

func intEnv(getenv Getenv, key string, def int) (int, error) {
	v := getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func msEnv(getenv Getenv, key string, def time.Duration) (time.Duration, error) {
	n, err := intEnv(getenv, key, int(def/time.Millisecond))
	if err != nil {
		return def, err
	}
	if n < 0 {
		return def, fmt.Errorf("%s: must be >= 0", key)
	}
	return time.Duration(n) * time.Millisecond, nil
}

func durEnv(getenv Getenv, key string, def time.Duration) (time.Duration, error) {
	v := getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func boolEnv(getenv Getenv, key string, def bool) (bool, error) {
	v := strings.ToLower(getenv(key))
	switch v {
	case "":
		return def, nil
	case "on", "true", "1", "yes":
		return true, nil
	case "off", "false", "0", "no":
		return false, nil
	}
	return def, fmt.Errorf("%s: invalid boolean %q", key, v)
}

func listEnv(getenv Getenv, key string) []string {
	v := getenv(key)
	if v == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
