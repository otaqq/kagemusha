package metrics

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	var b strings.Builder
	if _, err := m.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestMetrics_CountersAndHistogram(t *testing.T) {
	m := New()
	m.Request("rendered")
	m.Request("rendered")
	m.Request("queue_timeout")
	m.Render(200, "ready", 1500*time.Millisecond)
	m.Render(200, "ready", 4*time.Second)
	m.Render(404, "idle", 100*time.Second)
	out := scrape(t, m)
	for _, want := range []string{
		"# TYPE kagemusha_requests_total counter\n",
		`kagemusha_requests_total{outcome="queue_timeout"} 1` + "\n" + `kagemusha_requests_total{outcome="rendered"} 2`,
		`kagemusha_renders_total{code="200",trigger="ready"} 2` + "\n" + `kagemusha_renders_total{code="404",trigger="idle"} 1`,
		"# TYPE kagemusha_render_duration_seconds histogram\n",
		`kagemusha_render_duration_seconds_bucket{le="1"} 0`,
		`kagemusha_render_duration_seconds_bucket{le="2"} 1`,
		`kagemusha_render_duration_seconds_bucket{le="5"} 2`,
		`kagemusha_render_duration_seconds_bucket{le="60"} 2`,
		`kagemusha_render_duration_seconds_bucket{le="+Inf"} 3`,
		"kagemusha_render_duration_seconds_sum 105.5\n",
		"kagemusha_render_duration_seconds_count 3\n",
		"# TYPE process_start_time_seconds gauge\nprocess_start_time_seconds ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestMetrics_ValuesReadAtScrapeTime(t *testing.T) {
	m := New()
	waiting := 3.0
	m.Gauge("kagemusha_queue_waiting", "Renders waiting for a slot.", func() float64 { return waiting })
	m.Counter("kagemusha_worker_renders_total", "Renders by this worker.", func() float64 { return 7 })
	m.LabeledCounter("kagemusha_browser_restarts_total", "Browser restarts by reason.", "reason", func() map[string]float64 {
		return map[string]float64{"watchdog": 1, "planned": 4}
	})
	waiting = 5
	out := scrape(t, m)
	for _, want := range []string{
		"# HELP kagemusha_queue_waiting Renders waiting for a slot.\n# TYPE kagemusha_queue_waiting gauge\nkagemusha_queue_waiting 5\n",
		"# TYPE kagemusha_worker_renders_total counter\nkagemusha_worker_renders_total 7\n",
		`kagemusha_browser_restarts_total{reason="planned"} 4` + "\n" + `kagemusha_browser_restarts_total{reason="watchdog"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestMetrics_LabelValuesAreEscaped(t *testing.T) {
	m := New()
	m.Request("a\"b\\c\nd\te")
	if out := scrape(t, m); !strings.Contains(out, `kagemusha_requests_total{outcome="a\"b\\c\nde"} 1`) {
		t.Fatalf("label escaping:\n%s", out)
	}
}

func TestMetrics_NilIsANoOp(t *testing.T) {
	var m *Metrics
	m.Request("x")
	m.Render(200, "ready", time.Second)
	m.Gauge("g", "h", func() float64 { return 1 })
	if n, err := m.WriteTo(nil); n != 0 || err != nil {
		t.Fatal("nil Metrics must write nothing")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestMetrics_WriteErrorIsReturned(t *testing.T) {
	if _, err := New().WriteTo(failWriter{}); err == nil {
		t.Fatal("want write error")
	}
}
