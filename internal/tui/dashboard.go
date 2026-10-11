package tui

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/pvanek/scwpod/internal/core"
)

// --- fetching ---

// fetchMetricsWhenReady fetches metrics once both the container and the
// database lists have arrived, so one refresh queries Cockpit once.
func (m *Model) fetchMetricsWhenReady() tea.Cmd {
	if m.containers.loading || m.databases.loading {
		return nil
	}
	return m.fetchMetrics()
}

func (m *Model) fetchMetrics() tea.Cmd {
	containers, dbs, now := m.containers.data, m.databases.data, m.now()
	if len(containers) == 0 && len(dbs) == 0 {
		return nil
	}
	m.metrics.loading = true
	return fetch(func(ctx context.Context) (core.Metrics, error) {
		return m.svc.Metrics(ctx, containers, dbs, now)
	})
}

// createTokens creates the Cockpit tokens the dashboard reported missing.
// It only runs on an explicit key press, because it changes the account.
func (m *Model) createTokens() tea.Cmd {
	missing := m.metrics.data.MissingTokens
	if len(missing) == 0 || m.tokens.loading {
		return nil
	}
	m.tokens.loading = true
	return fetch(func(ctx context.Context) (tokensCreated, error) {
		return tokensCreated{}, m.svc.CreateCockpitTokens(ctx, missing)
	})
}

// --- header ---

var (
	tileStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
	tileValue = lipgloss.NewStyle().Bold(true)
)

func (m Model) dashboardHeader() string {
	scope := m.scopeID()
	containers := core.ByProject(m.containers.data, scope, func(c core.Container) string { return c.ProjectID })
	dbs := core.ByProject(m.databases.data, scope, func(d core.Database) string { return d.ProjectID })

	readyC, failedC, running, idle, known := 0, 0, 0, 0, false
	for _, c := range containers {
		switch c.Status {
		case "ready":
			readyC++
		case "error":
			failedC++
		}
		if n, ok := m.runningInstances(c); ok {
			known = true
			if n > 0 {
				running++
			} else {
				idle++
			}
		}
	}
	readyD := 0
	for _, d := range dbs {
		if d.Status == "ready" {
			readyD++
		}
	}

	contValue := fmt.Sprintf("%d/%d ready", readyC, len(containers))
	if known {
		contValue = fmt.Sprintf("%d running · %d idle", running, idle)
	}
	if failedC > 0 {
		contValue += warn.Render(fmt.Sprintf(" %d err", failedC))
	}
	tiles := []struct{ title, value string }{
		{"Containers", contValue},
		{"Databases", fmt.Sprintf("%d/%d ready", readyD, len(dbs))},
	}

	rendered := make([]string, len(tiles))
	compact := make([]string, len(tiles))
	for i, t := range tiles {
		rendered[i] = tileStyle.Render(dim.Render(t.title) + "\n" + tileValue.Render(t.value))
		compact[i] = dim.Render(t.title+" ") + tileValue.Render(t.value)
	}
	head := lipgloss.JoinHorizontal(lipgloss.Top, rendered...)
	if lipgloss.Width(head) > m.width {
		head = strings.Join(compact, dim.Render(" · "))
		head = lipgloss.NewStyle().Width(max(m.width, 20)).Render(head)
	}

	lines := []string{head}
	if missing := m.metrics.data.MissingTokens; len(missing) > 0 && !m.tokens.loading {
		scopes := make([]string, len(missing))
		for i, pr := range missing {
			scopes[i] = m.projectName(pr.ProjectID) + " (" + pr.Region + ")"
		}
		note := "Metrics need a read-only Cockpit token for " + strings.Join(scopes, ", ") +
			". Press T to create them (saved to ~/.config/scwpod/cockpit-tokens.json)."
		lines = append(lines, warn.Width(max(m.width, 20)).Render(note))
	}
	return strings.Join(lines, "\n")
}

func fmtDuration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return d.String()
}

// --- table ---

// minColWidth is the narrowest a dashboard column may get, padding included.
const minColWidth = 8

// minWidthForLogs is the screen width from which the Overview tables get a
// Logs link column; narrower screens use the L key.
const minWidthForLogs = 90

// metricCol is one metric shown for a resource type: a number in an
// Overview table column and a chart in the detail view.
type metricCol struct {
	kind   core.MetricKind
	title  string // column title
	label  string // detail label
	min    int
	weight int // share of leftover width
	// text, when set, renders the value as plain text instead of a chart.
	text func(core.ResourceMetrics) (string, bool)
}

var containerMetricCols = []metricCol{
	{core.MetricCPU, "CPU", "CPU", 5, 1, nil},
	{core.MetricMemory, "Mem", "Memory", 5, 1, nil},
	{core.MetricInstances, "Inst", "Instances", 4, 1, nil},
	{core.MetricRequests, "Req/s", "Requests", 7, 1, nil},
	{core.MetricErrorRate, "5xx", "Error rate (5xx)", 5, 1, nil},
	{core.MetricLatency, "p95", "Latency p95", 6, 1, nil},
}

var databaseMetricCols = []metricCol{
	{core.MetricCPU, "CPU", "CPU", 5, 1, nil},
	{core.MetricMemory, "Mem", "Memory", 5, 1, nil},
	{core.MetricDisk, "Disk", "Disk", 5, 1, nil},
	{core.MetricConnections, "Conn", "Connections", 7, 1, connectionsText},
	{core.MetricReplicationLag, "Lag", "Replication lag", 5, 1, nil},
	{core.MetricWAL, "WAL", "WAL", 8, 1, nil},
	{core.MetricTransactions, "TPS", "Transactions/queries", 7, 1, nil},
	{core.MetricCacheHit, "Cache", "Cache hit ratio", 5, 1, nil},
}

// metricTable builds a pane with fixed leading columns followed by one
// column per metric.
func (m Model) metricTable(title string, fixed []col, metrics []metricCol, n int,
	row func(i int) ([]string, core.ResourceMetrics, any)) paneData {
	// Keep columns readable on narrow screens (Termux) by dropping the
	// lowest-priority metrics, which are last in the list; the detail view
	// still shows all of them.
	metrics = withData(metrics, n, func(i int) core.ResourceMetrics { _, rm, _ := row(i); return rm })
	if fit := m.width/minColWidth - len(fixed); fit < len(metrics) {
		metrics = metrics[:max(fit, 1)]
	}
	cols := append([]col(nil), fixed...)
	for _, mc := range metrics {
		cols = append(cols, col{mc.title, mc.min, mc.weight})
	}
	logsCol := m.width >= minWidthForLogs
	if logsCol {
		cols = append(cols, col{"Logs", 6, 0})
	}
	d := paneData{title: title, cols: cols}
	for i := range n {
		cells, rm, item := row(i)
		links := m.links(item)
		// The name opens the metrics dashboard.
		cells[0] = linkCell(cells[0], links.Metrics)
		for _, mc := range metrics {
			cells = append(cells, metricCell(rm, mc))
		}
		if logsCol {
			logs := "–"
			if links.Logs != "" {
				logs = linkCell("logs ↗", links.Logs)
			}
			cells = append(cells, logs)
		}
		d.rows = append(d.rows, table.Row(cells))
		d.items = append(d.items, item)
	}
	return d
}

// withData drops metric columns that no row has data for, e.g. latency when
// the account reports no latency histogram. Without any metrics at all
// (none loaded yet, or no token) every column is kept.
func withData(metrics []metricCol, n int, rm func(int) core.ResourceMetrics) []metricCol {
	var out []metricCol
	for _, mc := range metrics {
		for i := range n {
			if _, ok := rm(i)[mc.kind].Last(); ok {
				out = append(out, mc)
				break
			}
		}
	}
	if len(out) == 0 {
		return metrics
	}
	return out
}

func (m Model) dashboardPanes(scope string) []paneData {
	containers := core.ByProject(m.containers.data, scope, func(c core.Container) string { return c.ProjectID })
	dbs := core.ByProject(m.databases.data, scope, func(d core.Database) string { return d.ProjectID })
	return []paneData{
		m.metricTable("Containers",
			[]col{{"Name", 10, 2}, {"Project", 8, 1}, {"Status", 10, 0}},
			containerMetricCols, len(containers),
			func(i int) ([]string, core.ResourceMetrics, any) {
				c := containers[i]
				return []string{c.Name, m.projectName(c.ProjectID), m.containerState(c)}, m.containerMetrics(c), c
			}),
		m.metricTable("Databases",
			[]col{{"Name", 10, 2}, {"Project", 8, 1}, {"Engine", 6, 0}, {"Status", 7, 0}},
			databaseMetricCols, len(dbs),
			func(i int) ([]string, core.ResourceMetrics, any) {
				d := dbs[i]
				return []string{d.Name, m.projectName(d.ProjectID), shortEngine(d.Engine), d.Status}, m.metrics.data.ByResource[d.ID], d
			}),
	}
}

// runningInstances reports how many instances of a container are running,
// from Cockpit, without contacting the container. ok is false when it is not
// known (no token, metrics not loaded yet or not provided).
func (m Model) runningInstances(c core.Container) (n int, ok bool) {
	v, ok := m.metrics.data.ByResource[c.ID][core.MetricInstances].Last()
	return int(math.Round(v)), ok
}

// containerState is the API status, refined for ready containers into
// "running ×N" or "idle" (scaled to zero) when the instance count is known.
func (m Model) containerState(c core.Container) string {
	if c.Status != "ready" {
		if c.ErrorReason != "" {
			return c.Status + " !"
		}
		return c.Status
	}
	n, ok := m.runningInstances(c)
	switch {
	case !ok:
		return c.Status
	case n > 0:
		return fmt.Sprintf("running ×%d", n)
	default:
		return "idle"
	}
}

// containerMetrics returns a container's metrics with CPU and memory turned
// into a percentage of the container's limits when the limits are known.
func (m Model) containerMetrics(c core.Container) core.ResourceMetrics {
	rm := m.metrics.data.ByResource[c.ID]
	if rm == nil {
		return nil
	}
	out := core.ResourceMetrics{}
	for kind, mt := range rm {
		switch {
		case kind == core.MetricCPU && mt.Unit == core.UnitCores && c.MilliVCPU > 0:
			mt = scaleMetric(mt, 100/(float64(c.MilliVCPU)/1000), core.UnitPercent)
		case kind == core.MetricMemory && mt.Unit == core.UnitBytes && c.MemoryMB > 0:
			mt = scaleMetric(mt, 100/(float64(c.MemoryMB)*1e6), core.UnitPercent)
		}
		out[kind] = mt
	}
	return out
}

func scaleMetric(mt core.Metric, factor float64, unit core.Unit) core.Metric {
	pts := make([]core.Point, len(mt.Points))
	for i, p := range mt.Points {
		pts[i] = core.Point{T: p.T, V: p.V * factor}
	}
	return core.Metric{Source: mt.Source, Unit: unit, Points: pts}
}

// metricCell renders the current value of a metric, or "–" without data.
func metricCell(rm core.ResourceMetrics, mc metricCol) string {
	if mc.text != nil {
		if s, ok := mc.text(rm); ok {
			return s
		}
		return "–"
	}
	mt, ok := rm[mc.kind]
	last, has := mt.Last()
	if !ok || !has {
		return "–"
	}
	return fmtValue(last, mt.Unit)
}

// connectionsText shows open connections against the configured maximum,
// e.g. "14/100". A chart of a mostly constant count says little.
func connectionsText(rm core.ResourceMetrics) (string, bool) {
	cur, ok := rm[core.MetricConnections].Last()
	if !ok {
		return "", false
	}
	if limit, ok := rm[core.MetricMaxConnections].Last(); ok && limit > 0 {
		return fmt.Sprintf("%.0f/%.0f", cur, limit), true
	}
	return fmt.Sprintf("%.0f", cur), true
}

const sparkTicks = "▁▂▃▄▅▆▇█"

// sparkline draws values in width cells. Percentages use a fixed 0–100
// scale so rows are comparable; other units scale to their own maximum.
func sparkline(values []float64, width int, unit core.Unit) string {
	values = core.Downsample(values, width)
	top := 100.0
	if unit != core.UnitPercent {
		top = 0
		for _, v := range values {
			top = max(top, v)
		}
	}
	if top <= 0 {
		top = 1
	}
	ticks := []rune(sparkTicks)
	var b strings.Builder
	for _, v := range values {
		i := int(math.Round(v / top * float64(len(ticks)-1)))
		b.WriteRune(ticks[min(max(i, 0), len(ticks)-1)])
	}
	return b.String()
}

func fmtValue(v float64, unit core.Unit) string {
	switch unit {
	case core.UnitPercent:
		return fmt.Sprintf("%.0f%%", v)
	case core.UnitBytes:
		return fmtBytes(v)
	case core.UnitCores:
		return fmt.Sprintf("%.2f", v)
	case core.UnitPerSec:
		return fmt.Sprintf("%.1f/s", v)
	case core.UnitSeconds:
		if v < 1 {
			return fmt.Sprintf("%.0fms", v*1000)
		}
		return fmt.Sprintf("%.1fs", v)
	case core.UnitBytesPS:
		return fmtBytes(v) + "/s"
	}
	return fmt.Sprintf("%.0f", v)
}

func fmtBytes(v float64) string {
	const unit = 1000.0
	if v < unit {
		return fmt.Sprintf("%.0fB", v)
	}
	exp := 0
	for v >= unit && exp < 4 {
		v /= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", v, "kMGT"[exp-1])
}

// metricsSection adds the last-hour charts of a resource to a detail sheet.
func metricsSection(s *sheet, rm core.ResourceMetrics, metrics []metricCol) {
	s.section("Metrics, last " + fmtDuration(core.MetricsWindow))
	if len(rm) == 0 {
		s.field("", dim.Render("no metrics"))
		return
	}
	width := max(s.width-labelWidth-24, 8)
	for _, mc := range metrics {
		if mc.text != nil {
			if text, ok := mc.text(rm); ok {
				s.field(mc.label, text)
			}
			continue
		}
		mt, ok := rm[mc.kind]
		last, has := mt.Last()
		if !ok || !has {
			continue // not provided for this resource
		}
		peak := 0.0
		for _, v := range mt.Values() {
			peak = max(peak, v)
		}
		s.field(mc.label, fmt.Sprintf("%s %s %s", sparkline(mt.Values(), width, mt.Unit),
			fmtValue(last, mt.Unit), dim.Render("max "+fmtValue(peak, mt.Unit))))
	}
}

// shortEngine abbreviates "PostgreSQL-16" to "pg16" and "MySQL-8" to "mysql8".
func shortEngine(engine string) string {
	name, version, _ := strings.Cut(engine, "-")
	if strings.EqualFold(name, "postgresql") {
		name = "pg"
	}
	return strings.ToLower(name) + version
}
