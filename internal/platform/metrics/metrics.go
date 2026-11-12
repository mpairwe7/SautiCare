// Package metrics is a minimal Prometheus text-format registry (counters
// and gauges), standard library only. It covers M0's needs until the
// OpenTelemetry Go SDK is adopted with tracing (ADR-0017).
package metrics

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

type kind string

const (
	kindCounter kind = "counter"
	kindGauge   kind = "gauge"
)

type metric struct {
	name, help string
	kind       kind
	bits       atomic.Uint64 // float64 bits
}

func (m *metric) load() float64 { return math.Float64frombits(m.bits.Load()) }

// Counter only goes up.
type Counter struct{ m *metric }

// Add increases the counter by a non-negative amount.
func (c *Counter) Add(v float64) {
	if v < 0 {
		return
	}
	for {
		old := c.m.bits.Load()
		if c.m.bits.CompareAndSwap(old, math.Float64bits(math.Float64frombits(old)+v)) {
			return
		}
	}
}

// Gauge is set to the current value.
type Gauge struct{ m *metric }

// Set stores v.
func (g *Gauge) Set(v float64) { g.m.bits.Store(math.Float64bits(v)) }

// Registry holds metrics and serves them.
type Registry struct {
	mu      sync.Mutex
	metrics map[string]*metric
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{metrics: map[string]*metric{}} }

func (r *Registry) add(name, help string, k kind) *metric {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.metrics[name]; dup {
		panic(fmt.Sprintf("metrics: duplicate metric %q", name))
	}
	m := &metric{name: name, help: help, kind: k}
	r.metrics[name] = m
	return m
}

// Counter registers a counter.
func (r *Registry) Counter(name, help string) *Counter {
	return &Counter{r.add(name, help, kindCounter)}
}

// Gauge registers a gauge.
func (r *Registry) Gauge(name, help string) *Gauge { return &Gauge{r.add(name, help, kindGauge)} }

// Handler serves the Prometheus text exposition format.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		r.mu.Lock()
		names := make([]string, 0, len(r.metrics))
		for n := range r.metrics {
			names = append(names, n)
		}
		r.mu.Unlock()
		sort.Strings(names)
		var b strings.Builder
		for _, n := range names {
			m := r.metrics[n]
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n%s %s\n",
				m.name, m.help, m.name, m.kind, m.name, strconv.FormatFloat(m.load(), 'g', -1, 64))
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
	})
}
