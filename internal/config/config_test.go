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
