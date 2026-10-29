// Package metrics collects kagemusha's metrics and writes them in the Prometheus text format
// (no client library: the set is small and fixed). All methods are safe on a nil *Metrics.
package metrics

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DurationBuckets are the render duration histogram bounds in seconds.
var DurationBuckets = []float64{0.5, 1, 2, 3, 5, 8, 13, 20, 30, 60}

// Metrics holds counters updated by the HTTP layer and values read from other components at scrape time.
type Metrics struct {
	start time.Time

	mu       sync.Mutex
	requests map[string]uint64    // outcome
	renders  map[[2]string]uint64 // code, trigger
	buckets  []uint64
	durSum   float64
	durCount uint64
	funcs    []sample
}

// sample is a value read at scrape time from another component.
type sample struct {
	name, help, typ, label string
	read                   func() map[string]float64 // label value → value; "" when unlabeled
}

// New creates an empty Metrics.
func New() *Metrics {
	return &Metrics{
		start:    time.Now(),
		requests: map[string]uint64{},
		renders:  map[[2]string]uint64{},
		buckets:  make([]uint64, len(DurationBuckets)),
	}
}

// Request counts one finished request by outcome (rendered, cache_hit, queue_timeout, ...).
func (m *Metrics) Request(outcome string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.requests[outcome]++
	m.mu.Unlock()
}

// Render records one actual render (shared single-flight results are counted once).
func (m *Metrics) Render(code int, trigger string, d time.Duration) {
	if m == nil {
		return
	}
	s := d.Seconds()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.renders[[2]string{strconv.Itoa(code), trigger}]++
	for i, b := range DurationBuckets {
		if s <= b {
			m.buckets[i]++
		}
	}
	m.durSum += s
	m.durCount++
}

// Gauge registers a value read at scrape time.
func (m *Metrics) Gauge(name, help string, read func() float64) {
	m.register(sample{name: name, help: help, typ: "gauge", read: func() map[string]float64 { return map[string]float64{"": read()} }})
}

// Counter registers a monotonically increasing value read at scrape time.
func (m *Metrics) Counter(name, help string, read func() float64) {
	m.register(sample{name: name, help: help, typ: "counter", read: func() map[string]float64 { return map[string]float64{"": read()} }})
}

// LabeledCounter registers counters with one label, read at scrape time.
func (m *Metrics) LabeledCounter(name, help, label string, read func() map[string]float64) {
	m.register(sample{name: name, help: help, typ: "counter", label: label, read: read})
}

func (m *Metrics) register(s sample) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.funcs = append(m.funcs, s)
	m.mu.Unlock()
}

// WriteTo writes all metrics in the Prometheus text exposition format (version 0.0.4).
func (m *Metrics) WriteTo(w io.Writer) (int64, error) {
	if m == nil {
		return 0, nil
	}
	var b strings.Builder
	m.mu.Lock()
	header(&b, "kagemusha_requests_total", "Finished requests by outcome.", "counter")
	for _, k := range sortedKeys(m.requests) {
		fmt.Fprintf(&b, "kagemusha_requests_total{outcome=%q} %d\n", escape(k), m.requests[k])
	}
	header(&b, "kagemusha_renders_total", "Actual renders by response status and completion trigger.", "counter")
	keys := make([][2]string, 0, len(m.renders))
	for k := range m.renders {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b [2]string) int { return strings.Compare(a[0]+"\x00"+a[1], b[0]+"\x00"+b[1]) })
	for _, k := range keys {
		fmt.Fprintf(&b, "kagemusha_renders_total{code=%q,trigger=%q} %d\n", escape(k[0]), escape(k[1]), m.renders[k])
	}
	header(&b, "kagemusha_render_duration_seconds", "Duration of actual renders.", "histogram")
	for i, le := range DurationBuckets {
		fmt.Fprintf(&b, "kagemusha_render_duration_seconds_bucket{le=%q} %d\n", strconv.FormatFloat(le, 'g', -1, 64), m.buckets[i])
	}
	fmt.Fprintf(&b, "kagemusha_render_duration_seconds_bucket{le=\"+Inf\"} %d\n", m.durCount)
	fmt.Fprintf(&b, "kagemusha_render_duration_seconds_sum %s\n", strconv.FormatFloat(m.durSum, 'g', -1, 64))
	fmt.Fprintf(&b, "kagemusha_render_duration_seconds_count %d\n", m.durCount)
	funcs := slices.Clone(m.funcs)
	m.mu.Unlock()

	for _, s := range funcs {
		header(&b, s.name, s.help, s.typ)
		vals := s.read()
		for _, k := range sortedKeys(vals) {
			v := strconv.FormatFloat(vals[k], 'g', -1, 64)
			if s.label == "" {
				fmt.Fprintf(&b, "%s %s\n", s.name, v)
			} else {
				fmt.Fprintf(&b, "%s{%s=%q} %s\n", s.name, s.label, escape(k), v)
			}
		}
	}
	header(&b, "process_start_time_seconds", "Start time of the process since the Unix epoch in seconds.", "gauge")
	fmt.Fprintf(&b, "process_start_time_seconds %d\n", m.start.Unix())
	n, err := io.WriteString(w, b.String())
	return int64(n), err
}

func header(b *strings.Builder, name, help, typ string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

// escape prepares a label value for %q, which already escapes backslash, quote and newline.
// Prometheus accepts exactly those escapes, so other control characters are dropped.
func escape(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
