// Package config loads settings from the environment. Every setting has a safe default.
package config

import (
	"fmt"
	"os"
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
}

// Default returns the default settings.
func Default() Config {
	return Config{
		Engine:               EngineChrome,
		Port:                 3000,
		MaxConcurrent:        4,
		QueueTimeout:         45 * time.Second,
		ReadyFlag:            true,
		IdleQuiet:            3 * time.Second,
		PageTimeout:          30 * time.Second,
		CacheControl:         "no-store",
		WorkerMaxRenders:     1000,
		WorkerMaxAge:         6 * time.Hour,
		BrowserRestartPeriod: 10 * time.Minute,
		WatchdogTimeout:      10 * time.Second,
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
	return c, nil
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
