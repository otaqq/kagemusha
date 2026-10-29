package config

import (
	"testing"
	"time"
)

func env(m map[string]string) Getenv { return func(k string) string { return m[k] } }

func TestLoad_Defaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Engine != EngineChrome || c.Port != 3000 || c.MaxConcurrent != 4 || c.QueueTimeout != 45*time.Second ||
		!c.ReadyFlag || c.CacheControl != "no-store" || c.WorkerMaxAge != 6*time.Hour {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"KAGEMUSHA_ENGINE":                 "lightpanda",
		"KAGEMUSHA_ENGINE_PATH":            "/usr/bin/lightpanda",
		"KAGEMUSHA_PORT":                   "8080",
		"KAGEMUSHA_MAX_CONCURRENT":         "2",
		"KAGEMUSHA_QUEUE_TIMEOUT_MS":       "1000",
		"KAGEMUSHA_READY_FLAG":             "off",
		"KAGEMUSHA_IDLE_QUIET_MS":          "500",
		"KAGEMUSHA_PAGE_TIMEOUT_MS":        "9000",
		"KAGEMUSHA_BLOCK_DOMAINS":          "a.example, b.example ,",
		"KAGEMUSHA_ALLOW_DOMAINS":          "shop.example",
		"KAGEMUSHA_DENY_PATHS":             "/api,/admin",
		"KAGEMUSHA_CACHE_CONTROL":          "public, max-age=60",
		"KAGEMUSHA_WORKER_MAX_RENDERS":     "10",
		"KAGEMUSHA_WORKER_MAX_AGE":         "1h30m",
		"KAGEMUSHA_BROWSER_RESTART_PERIOD": "5m",
		"KAGEMUSHA_WATCHDOG_TIMEOUT_MS":    "2000",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Engine != EngineLightpanda || c.EnginePath != "/usr/bin/lightpanda" || c.Port != 8080 || c.MaxConcurrent != 2 ||
		c.QueueTimeout != time.Second || c.ReadyFlag || c.IdleQuiet != 500*time.Millisecond || c.PageTimeout != 9*time.Second ||
		len(c.BlockDomains) != 2 || c.BlockDomains[1] != "b.example" || len(c.AllowDomains) != 1 || len(c.DenyPaths) != 2 ||
		c.CacheControl != "public, max-age=60" || c.WorkerMaxRenders != 10 || c.WorkerMaxAge != 90*time.Minute ||
		c.BrowserRestartPeriod != 5*time.Minute || c.WatchdogTimeout != 2*time.Second {
		t.Fatalf("unexpected: %+v", c)
	}
}

func TestLoad_InvalidValuesFail(t *testing.T) {
	cases := []map[string]string{
		{"KAGEMUSHA_ENGINE": "firefox"},
		{"KAGEMUSHA_PORT": "abc"},
		{"KAGEMUSHA_MAX_CONCURRENT": "0"},
		{"KAGEMUSHA_QUEUE_TIMEOUT_MS": "-1"},
		{"KAGEMUSHA_READY_FLAG": "maybe"},
		{"KAGEMUSHA_WORKER_MAX_AGE": "soon"},
		{"KAGEMUSHA_IDLE_QUIET_MS": "x"},
		{"KAGEMUSHA_PAGE_TIMEOUT_MS": "x"},
		{"KAGEMUSHA_WORKER_MAX_RENDERS": "x"},
		{"KAGEMUSHA_BROWSER_RESTART_PERIOD": "x"},
		{"KAGEMUSHA_WATCHDOG_TIMEOUT_MS": "x"},
		{"KAGEMUSHA_MAX_CONCURRENT": "x"},
	}
	for _, m := range cases {
		if _, err := Load(env(m)); err == nil {
			t.Errorf("expected error for %v", m)
		}
	}
}

func TestLoad_NilGetenvUsesOsGetenv(t *testing.T) {
	t.Setenv("KAGEMUSHA_PORT", "4000")
	c, err := Load(nil)
	if err != nil || c.Port != 4000 {
		t.Fatalf("got %v %+v", err, c)
	}
}

func TestLoad_SecurityDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenHost != "" || c.TargetHosts != nil || c.AllowPrivateTargets || c.ForwardHeaders != nil ||
		c.ForwardHeadersHosts != nil || c.ResponseHeaders != nil || c.UserAgent != "" {
		t.Fatalf("security settings must default to off / deny-private: %+v", c)
	}
}

func TestLoad_SecuritySettings(t *testing.T) {
	c, err := Load(env(map[string]string{
		"KAGEMUSHA_LISTEN_HOST":           "127.0.0.1",
		"KAGEMUSHA_TARGET_HOSTS":          "shop.example, stg.example",
		"KAGEMUSHA_ALLOW_PRIVATE_TARGETS": "on",
		"KAGEMUSHA_FORWARD_HEADERS":       "authorization, x-api-key",
		"KAGEMUSHA_FORWARD_HEADERS_HOSTS": "api.example",
		"KAGEMUSHA_RESPONSE_HEADERS":      "X-Robots-Tag: noindex, nofollow\n\n  X-Env: stg  \n",
		"KAGEMUSHA_USER_AGENT":            "MyRenderer/1.0",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenHost != "127.0.0.1" || len(c.TargetHosts) != 2 || !c.AllowPrivateTargets ||
		len(c.ForwardHeaders) != 2 || c.ForwardHeaders[0] != "Authorization" || c.ForwardHeaders[1] != "X-Api-Key" ||
		len(c.ForwardHeadersHosts) != 1 || c.UserAgent != "MyRenderer/1.0" {
		t.Fatalf("unexpected: %+v", c)
	}
	if c.ResponseHeaders.Get("X-Robots-Tag") != "noindex, nofollow" || c.ResponseHeaders.Get("X-Env") != "stg" || len(c.ResponseHeaders) != 2 {
		t.Fatalf("response headers: %v", c.ResponseHeaders)
	}
}

func TestLoad_InvalidSecuritySettingsFail(t *testing.T) {
	cases := []map[string]string{
		{"KAGEMUSHA_ALLOW_PRIVATE_TARGETS": "maybe"},
		{"KAGEMUSHA_FORWARD_HEADERS": "Host"},
		{"KAGEMUSHA_FORWARD_HEADERS": "proxy-authorization"},
		{"KAGEMUSHA_FORWARD_HEADERS": "Bad Header"},
		{"KAGEMUSHA_RESPONSE_HEADERS": "no-colon"},
		{"KAGEMUSHA_RESPONSE_HEADERS": "X-Empty:"},
		{"KAGEMUSHA_RESPONSE_HEADERS": "Bad Name: x"},
		{"KAGEMUSHA_RESPONSE_HEADERS": "cache-control: public"},
		{"KAGEMUSHA_RESPONSE_HEADERS": "X-Kagemusha-Trigger: ready"},
	}
	for _, m := range cases {
		if _, err := Load(env(m)); err == nil {
			t.Errorf("expected error for %v", m)
		}
	}
}

func TestLoad_BlankResponseHeadersIsNil(t *testing.T) {
	c, err := Load(env(map[string]string{"KAGEMUSHA_RESPONSE_HEADERS": " \n "}))
	if err != nil || c.ResponseHeaders != nil {
		t.Fatalf("got %v %v", err, c.ResponseHeaders)
	}
}

func TestLoad_StripQueryParams(t *testing.T) {
	c, err := Load(env(map[string]string{"KAGEMUSHA_STRIP_QUERY_PARAMS": "utm_*, fbclid ,gclid"}))
	if err != nil || len(c.StripQueryParams) != 3 || c.StripQueryParams[0] != "utm_*" || c.StripQueryParams[1] != "fbclid" {
		t.Fatalf("got %v %v", err, c.StripQueryParams)
	}
	for _, bad := range []string{"*", "utm_*x", "*_source", "a**"} {
		if _, err := Load(env(map[string]string{"KAGEMUSHA_STRIP_QUERY_PARAMS": bad})); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestLoad_Cache(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil || c.CacheTTL != 0 || c.CacheMaxEntries != 100 {
		t.Fatalf("cache must default to off: %v %+v", err, c)
	}
	c, err = Load(env(map[string]string{"KAGEMUSHA_CACHE_TTL": "5m", "KAGEMUSHA_CACHE_MAX_ENTRIES": "20"}))
	if err != nil || c.CacheTTL != 5*time.Minute || c.CacheMaxEntries != 20 {
		t.Fatalf("got %v %+v", err, c)
	}
	for _, m := range []map[string]string{
		{"KAGEMUSHA_CACHE_TTL": "soon"},
		{"KAGEMUSHA_CACHE_TTL": "-1s"},
		{"KAGEMUSHA_CACHE_MAX_ENTRIES": "x"},
		{"KAGEMUSHA_CACHE_MAX_ENTRIES": "0"},
	} {
		if _, err := Load(env(m)); err == nil {
			t.Errorf("expected error for %v", m)
		}
	}
}

func TestLoad_BlockResourceTypes(t *testing.T) {
	c, err := Load(env(map[string]string{"KAGEMUSHA_BLOCK_RESOURCE_TYPES": "image, MEDIA,font,xhr"}))
	if err != nil || len(c.BlockResourceTypes) != 4 || c.BlockResourceTypes[0] != "Image" || c.BlockResourceTypes[1] != "Media" ||
		c.BlockResourceTypes[2] != "Font" || c.BlockResourceTypes[3] != "XHR" {
		t.Fatalf("got %v %v", err, c.BlockResourceTypes)
	}
	for _, bad := range []string{"Document", "document", "Picture"} {
		if _, err := Load(env(map[string]string{"KAGEMUSHA_BLOCK_RESOURCE_TYPES": bad})); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}
