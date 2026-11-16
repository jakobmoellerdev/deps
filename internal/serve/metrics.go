package serve

import (
	"io"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Metrics is the Prometheus exposition of the daemon. It is written by hand rather than with
// client_golang: the project keeps a short dependency list, and a dozen counters plus one
// histogram need no registry.
type Metrics struct {
	mu         sync.Mutex
	counters   map[string]map[string]float64 // metric -> label set -> value
	histograms map[string]map[string]*histogram
	gauges     map[string]func() float64
}

// cycleBuckets bound a cycle's wall time; the measured range is seconds for a small
// repository to minutes for a monorepo with lock file regeneration.
var cycleBuckets = []float64{1, 2, 5, 10, 30, 60, 300, 900, 1800}

type histogram struct {
	counts []uint64
	sum    float64
	total  uint64
}

// NewMetrics returns an empty exposition.
func NewMetrics() *Metrics {
	return &Metrics{
		counters:   map[string]map[string]float64{},
		histograms: map[string]map[string]*histogram{},
		gauges:     map[string]func() float64{},
	}
}

// help and typ describe the metrics, so the exposition is self-documenting.
var metricHelp = map[string][2]string{
	"deps_cycles_total":                 {"counter", "Repository cycles by forge, mode and outcome."},
	"deps_cycle_duration_seconds":       {"histogram", "Wall time of one repository cycle."},
	"deps_queue_due":                    {"gauge", "Repositories due now, for this worker's buckets and for the fleet."},
	"deps_queue_claims_total":           {"counter", "Repositories claimed, from the worker's own buckets or stolen."},
	"deps_prs_total":                    {"counter", "Pull requests by what happened to them."},
	"deps_http_requests_total":          {"counter", "Upstream metadata requests by host and cache outcome."},
	"deps_rate_limited_total":           {"counter", "Lookups that hit a host's rate limit."},
	"deps_credential_budget_remaining":  {"gauge", "Requests left in the current hour of a credential's budget."},
	"deps_toolchain_installs_total":     {"counter", "Toolchain installations by tool and result."},
	"deps_webhooks_total":               {"counter", "Webhook deliveries by forge and what was done with them."},
	"deps_clone_cache_bytes":            {"gauge", "Size of the git mirror cache on disk."},
	"deps_tool_cache_bytes":             {"gauge", "Size of the toolchain cache on disk."},
	"deps_workers_live":                 {"gauge", "Workers that heartbeated recently."},
	"deps_worker_active_cycles":         {"gauge", "Cycles running in this replica right now."},
	"deps_repositories":                 {"gauge", "Repositories in the inventory."},
	"deps_scheduler_leader":             {"gauge", "1 while this replica holds the scheduler lock."},
	"deps_inventory_refresh_total":      {"counter", "Inventory refreshes by outcome."},
	"deps_inventory_repositories_total": {"counter", "Repositories added or disabled by the inventory refresh."},
	"deps_cycle_lease_lost_total":       {"counter", "Cycles whose result could not be recorded because the lease was gone."},
}

// Inc adds one to a counter.
func (m *Metrics) Inc(name string, labels map[string]string) { m.Add(name, labels, 1) }

// Add adds v to a counter.
func (m *Metrics) Add(name string, labels map[string]string, v float64) {
	key := labelKey(labels)
	m.mu.Lock()
	defer m.mu.Unlock()
	series, ok := m.counters[name]
	if !ok {
		series = map[string]float64{}
		m.counters[name] = series
	}
	series[key] += v
}

// Observe records a value in a histogram.
func (m *Metrics) Observe(name string, labels map[string]string, v float64) {
	key := labelKey(labels)
	m.mu.Lock()
	defer m.mu.Unlock()
	series, ok := m.histograms[name]
	if !ok {
		series = map[string]*histogram{}
		m.histograms[name] = series
	}
	h, ok := series[key]
	if !ok {
		h = &histogram{counts: make([]uint64, len(cycleBuckets))}
		series[key] = h
	}
	for i, b := range cycleBuckets {
		if v <= b {
			h.counts[i]++
		}
	}
	h.sum += v
	h.total++
}

// SetGauge registers a gauge read at scrape time, so nothing has to push it.
func (m *Metrics) SetGauge(name string, read func() float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gauges[name] = read
}

// Gauge is a simple settable gauge value.
type Gauge struct{ v atomic.Int64 }

func (g *Gauge) Set(v int64)  { g.v.Store(v) }
func (g *Gauge) Add(d int64)  { g.v.Add(d) }
func (g *Gauge) Get() float64 { return float64(g.v.Load()) }

// labelKey renders a label set canonically, sorted so one series has one key.
func labelKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := slices.Sorted(maps.Keys(labels))
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(escapeLabel(labels[k]))
		b.WriteString(`"`)
	}
	return b.String()
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escapeLabel(v string) string { return labelEscaper.Replace(v) }

// WriteTo renders the exposition in the Prometheus text format.
func (m *Metrics) WriteTo(w io.Writer) (int64, error) {
	m.mu.Lock()
	type gauge struct {
		name string
		read func() float64
	}
	gauges := make([]gauge, 0, len(m.gauges))
	for name, read := range m.gauges {
		gauges = append(gauges, gauge{name, read})
	}
	counters := map[string]map[string]float64{}
	for name, series := range m.counters {
		counters[name] = maps.Clone(series)
	}
	hists := map[string]map[string]histogram{}
	for name, series := range m.histograms {
		hists[name] = map[string]histogram{}
		for key, h := range series {
			hists[name][key] = histogram{counts: slices.Clone(h.counts), sum: h.sum, total: h.total}
		}
	}
	m.mu.Unlock()

	var b strings.Builder
	for _, name := range sorted(counters) {
		writeHeader(&b, name)
		for _, key := range sortedKeys(counters[name]) {
			writeSample(&b, name, key, counters[name][key])
		}
	}
	sort.Slice(gauges, func(i, j int) bool { return gauges[i].name < gauges[j].name })
	for _, g := range gauges {
		writeHeader(&b, g.name)
		writeSample(&b, g.name, "", g.read())
	}
	for _, name := range sortedHist(hists) {
		writeHeader(&b, name)
		for _, key := range sortedHistKeys(hists[name]) {
			h := hists[name][key]
			for i, bound := range cycleBuckets {
				writeSample(&b, name+"_bucket", withLabel(key, "le", formatFloat(bound)), float64(h.counts[i]))
			}
			writeSample(&b, name+"_bucket", withLabel(key, "le", "+Inf"), float64(h.total))
			writeSample(&b, name+"_sum", key, h.sum)
			writeSample(&b, name+"_count", key, float64(h.total))
		}
	}
	n, err := io.WriteString(w, b.String())
	return int64(n), err
}

func writeHeader(b *strings.Builder, name string) {
	if h, ok := metricHelp[name]; ok {
		b.WriteString("# HELP " + name + " " + h[1] + "\n")
		b.WriteString("# TYPE " + name + " " + h[0] + "\n")
	}
}

func writeSample(b *strings.Builder, name, labels string, v float64) {
	b.WriteString(name)
	if labels != "" {
		b.WriteString("{" + labels + "}")
	}
	b.WriteString(" " + formatFloat(v) + "\n")
}

func withLabel(key, name, value string) string {
	if key == "" {
		return name + `="` + value + `"`
	}
	return key + "," + name + `="` + value + `"`
}

func formatFloat(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func sorted(m map[string]map[string]float64) []string       { return slices.Sorted(maps.Keys(m)) }
func sortedKeys(m map[string]float64) []string              { return slices.Sorted(maps.Keys(m)) }
func sortedHist(m map[string]map[string]histogram) []string { return slices.Sorted(maps.Keys(m)) }
func sortedHistKeys(m map[string]histogram) []string        { return slices.Sorted(maps.Keys(m)) }
