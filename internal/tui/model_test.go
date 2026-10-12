package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/pvanek/scwpod/internal/core"
)

type fakeFetcher struct {
	createdTokens *[]core.ProjectRegion
}

func (fakeFetcher) Projects(context.Context) ([]core.Project, error) {
	return []core.Project{{ID: "p1", Name: "web"}, {ID: "p2", Name: "data"}}, nil
}

func (fakeFetcher) Containers(context.Context) ([]core.Container, error) {
	return []core.Container{
		{ID: "c1", Name: "api", ProjectID: "p1", Region: "fr-par", Status: "ready", MilliVCPU: 500, MemoryMB: 256},
		{ID: "c2", Name: "worker", ProjectID: "p2", Region: "fr-par", Status: "error", ErrorReason: "crash"},
	}, nil
}

func (fakeFetcher) Databases(context.Context) ([]core.Database, error) {
	return []core.Database{{ID: "d1", Name: "pg-main", ProjectID: "p2", Engine: "PostgreSQL-16", Status: "ready"}}, nil
}

func (fakeFetcher) Storage(context.Context) ([]core.StorageItem, error) {
	two := 2
	return []core.StorageItem{
		{Kind: core.StorageRegistry, ID: "r1", Name: "images", ProjectID: "p1", Location: "fr-par", Status: "ready",
			SizeBytes: 1.5e9, Images: &two, Endpoint: "rg.fr-par.scw.cloud/images"},
		{Kind: core.StorageBlock, ID: "v1", Name: "pgdata", ProjectID: "p2", Location: "fr-par-1", Status: "in_use",
			SizeBytes: 50e9, Class: "sbs_5k", IOPS: 5000, AttachedTo: []string{"db-vm"}},
		{Kind: core.StorageLocal, ID: "v2", Name: "root", ProjectID: "p2", Location: "fr-par-1", Status: "available",
			SizeBytes: 20e9, Class: "l_ssd"},
	}, errors.New("listing file systems in fr-par: permission denied")
}

func (fakeFetcher) Buckets(context.Context, []string) ([]core.StorageItem, error) {
	return []core.StorageItem{{Kind: core.StorageObject, ID: "assets", Name: "assets", ProjectID: "p1",
		Location: "nl-ams", Endpoint: "https://assets.s3.nl-ams.scw.cloud"}}, nil
}

func (fakeFetcher) Users(context.Context) ([]core.User, error) {
	return nil, errors.New("permission denied")
}

func (fakeFetcher) Billing(_ context.Context, period string, _ time.Time) (core.Billing, error) {
	month := func(m time.Month) time.Time { return time.Date(2026, m, 1, 0, 0, 0, 0, time.UTC) }
	return core.Billing{
		Spend: core.Spend{Period: period, Lines: []core.SpendLine{
			{ProjectID: "p1", ProjectName: "web", Category: "Compute", Amount: 3, Currency: "EUR"},
			{ProjectID: "p2", ProjectName: "data", Category: "Managed Databases", Amount: 7.5, Currency: "EUR"},
		}},
		Discount: 0.5,
		Tax:      2,
		Invoices: []core.Invoice{
			{Period: month(9), Taxed: 24, Currency: "EUR", State: "paid"},
			{Period: month(8), Taxed: 36, Currency: "EUR", State: "outdated"},
		},
	}, nil
}

func series(unit core.Unit, values ...float64) core.Metric {
	m := core.Metric{Unit: unit}
	for i, v := range values {
		m.Points = append(m.Points, core.Point{T: time.Unix(int64(i*60), 0), V: v})
	}
	return m
}

func (fakeFetcher) Metrics(context.Context, []core.Container, []core.Database, time.Time) (core.Metrics, error) {
	return core.Metrics{
		ByResource: map[string]core.ResourceMetrics{
			"c1": {
				core.MetricCPU:       series(core.UnitCores, 0.05, 0.25),        // of 0.5 vCPU -> 50%
				core.MetricMemory:    series(core.UnitBytes, 64e6, 128e6),       // of 256 MB -> 50%
				core.MetricRequests:  series(core.UnitPerSec, 1, 2, 3, 4, 5, 6), // req/s
				core.MetricErrorRate: series(core.UnitPercent, 0, 2.5),
				core.MetricLatency:   series(core.UnitSeconds, 0.12, 0.25),
				core.MetricInstances: series(core.UnitCount, 1, 3),
			},
			"d1": {
				core.MetricCPU:            series(core.UnitPercent, 10, 30),
				core.MetricDisk:           series(core.UnitPercent, 70, 71),
				core.MetricConnections:    series(core.UnitCount, 12, 14),
				core.MetricMaxConnections: series(core.UnitCount, 100, 100),
				core.MetricReplicationLag: series(core.UnitSeconds, 0.4, 2),
				core.MetricWAL:            series(core.UnitBytesPS, 1e6, 2.5e6),
				core.MetricCacheHit:       series(core.UnitPercent, 99, 98),
			},
		},
		MissingTokens: []core.ProjectRegion{{ProjectID: "p2", Region: "fr-par"}},
		Names:         map[string]string{"c1": "nsabc123-api"},
	}, nil
}

func (fakeFetcher) GrafanaURLs(context.Context, []string) (map[string]string, error) {
	return map[string]string{"p1": "https://p1.dashboard.example", "p2": "https://p2.dashboard.example"}, nil
}

func (f fakeFetcher) CreateCockpitTokens(_ context.Context, scopes []core.ProjectRegion) error {
	if f.createdTokens != nil {
		*f.createdTokens = append(*f.createdTokens, scopes...)
	}
	return nil
}

// loaded returns a model with all fake data delivered.
func loaded(t *testing.T) Model {
	t.Helper()
	m := New(fakeFetcher{})
	m.now = func() time.Time { return time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC) }
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	ctx := context.Background()
	f := fakeFetcher{}
	p, err := f.Projects(ctx)
	tm, _ = tm.Update(fetched[[]core.Project]{data: p, err: err})
	c, err := f.Containers(ctx)
	tm, _ = tm.Update(fetched[[]core.Container]{data: c, err: err})
	d, err := f.Databases(ctx)
	tm, _ = tm.Update(fetched[[]core.Database]{data: d, err: err})
	st, err := f.Storage(ctx)
	tm, _ = tm.Update(fetched[[]core.StorageItem]{data: st, err: err})
	bk, err := f.Buckets(ctx, nil)
	tm, _ = tm.Update(fetched[bucketList]{data: bk, err: err})
	u, err := f.Users(ctx)
	tm, _ = tm.Update(fetched[[]core.User]{data: u, err: err})
	b, err := f.Billing(ctx, "2026-10", m.now())
	tm, _ = tm.Update(fetched[core.Billing]{data: b, err: err})
	mt, err := f.Metrics(ctx, c, d, m.now())
	tm, _ = tm.Update(fetched[core.Metrics]{data: mt, err: err})
	g, err := f.GrafanaURLs(ctx, nil)
	tm, _ = tm.Update(fetched[map[string]string]{data: g, err: err})
	return tm.(Model)
}

func press(m Model, key string) Model {
	m.handleKey(key)
	m.rebuildTable()
	return m
}

func TestProjectsAllProjects(t *testing.T) {
	m := press(loaded(t), "1")
	out := m.render()
	for _, want := range []string{"All projects (2)", "Spend 2026-10: 10.50 EUR", "web", "data"} {
		if !strings.Contains(out, want) {
			t.Errorf("overview missing %q:\n%s", want, out)
		}
	}
}

func TestProjectScopeFilters(t *testing.T) {
	m := press(loaded(t), "p") // -> "web"
	m = press(m, "2")          // containers tab
	out := m.render()
	if !strings.Contains(out, "Project: ") || !strings.Contains(out, "api") {
		t.Errorf("expected web's container:\n%s", out)
	}
	if strings.Contains(out, "worker") {
		t.Errorf("container from other project leaked:\n%s", out)
	}
	m = press(m, "P") // back to all
	if m.scopeID() != core.AllProjects {
		t.Errorf("scope = %q, want all", m.scopeID())
	}
}

func TestTabErrorIsShown(t *testing.T) {
	m := press(loaded(t), "5") // users
	if out := m.render(); !strings.Contains(out, "permission denied") {
		t.Errorf("users error not shown:\n%s", out)
	}
	// Other tabs are unaffected.
	m = press(m, "3")
	if out := m.render(); strings.Contains(out, "permission denied") || !strings.Contains(out, "pg-main") {
		t.Errorf("databases tab wrong:\n%s", out)
	}
}

func resize(m Model, w, h int) Model {
	tm, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return tm.(Model)
}

func TestBillingPanel(t *testing.T) {
	m := resize(press(loaded(t), "6"), 170, 40)
	out := m.render()
	for _, want := range []string{
		"This month 2026-10", "Consumption", "10.50 EUR", "-0.50 EUR", "Total", "12.00 EUR",
		"2026-09", "24.00 EUR", "outdated", "Last 2 months", "60.00 EUR", "Monthly average", "30.00 EUR",
		"By category", "Managed Databases",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("billing panel missing %q:\n%s", want, out)
		}
	}
	// Scoped to one project: no org-level totals in the current-month box.
	m = press(m, "p")
	if out := m.render(); strings.Contains(out, "Discounts") || !strings.Contains(out, "org-wide") {
		t.Errorf("scoped billing panel wrong:\n%s", out)
	}
}

func TestBillingPanelNarrowStacks(t *testing.T) {
	m := resize(press(loaded(t), "6"), 60, 50)
	out := m.render()
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > 60 {
			t.Errorf("line %d is %d wide, want <= 60: %q", i, w, line)
		}
	}
	if !strings.Contains(out, "This month") || !strings.Contains(out, "Invoices") {
		t.Errorf("stacked panel missing boxes:\n%s", out)
	}
}

func sendKey(m Model, code rune) Model {
	tm, _ := m.Update(tea.KeyPressMsg{Code: code})
	return tm.(Model)
}

func TestArrowKeysMoveTableCursor(t *testing.T) {
	m := press(loaded(t), "6") // billing: 2 rows
	m = sendKey(m, tea.KeyDown)
	if got := m.cur().table.Cursor(); got != 1 {
		t.Fatalf("after down: cursor = %d, want 1", got)
	}
	m = sendKey(m, tea.KeyUp)
	if got := m.cur().table.Cursor(); got != 0 {
		t.Fatalf("after up: cursor = %d, want 0", got)
	}

	// A data refresh keeps the selection.
	m = sendKey(m, tea.KeyDown)
	b, _ := fakeFetcher{}.Billing(context.Background(), "2026-10", m.now())
	tm, _ := m.Update(fetched[core.Billing]{data: b})
	if got := tm.(Model).cur().table.Cursor(); got != 1 {
		t.Errorf("after refresh: cursor = %d, want 1", got)
	}
}

func TestEnterOpensDetailAndEscCloses(t *testing.T) {
	m := press(loaded(t), "2") // containers: api, worker
	m = sendKey(m, tea.KeyDown)
	m = sendKey(m, tea.KeyEnter)
	if m.detail == nil {
		t.Fatal("enter did not open detail")
	}
	out := m.render()
	for _, want := range []string{"Container worker", "crash", "data", "esc back"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Container api") {
		t.Errorf("detail shows wrong row:\n%s", out)
	}

	// Refreshed data replaces the shown item.
	tm, _ := m.Update(fetched[[]core.Container]{data: []core.Container{
		{ID: "c1", Name: "api", ProjectID: "p1", Status: "ready"},
		{ID: "c2", Name: "worker", ProjectID: "p2", Status: "ready"},
	}})
	m = tm.(Model)
	if out := m.render(); strings.Contains(out, "crash") {
		t.Errorf("detail not refreshed:\n%s", out)
	}

	m = sendKey(m, tea.KeyEscape)
	if m.detail != nil {
		t.Fatal("esc did not close detail")
	}
	if got := m.cur().table.Cursor(); got != 1 {
		t.Errorf("cursor after closing detail = %d, want 1", got)
	}
}

func TestProjectsEnterShowsProject(t *testing.T) {
	m := press(loaded(t), "1")
	m = sendKey(m, tea.KeyDown) // "data"
	m = sendKey(m, tea.KeyEnter)
	out := m.render()
	for _, want := range []string{"Project data", "pg-main", "worker", "Managed Databases", "7.50 EUR"} {
		if !strings.Contains(out, want) {
			t.Errorf("project detail missing %q:\n%s", want, out)
		}
	}
	// Switching tab closes the detail.
	tm, _ := m.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
	m = tm.(Model)
	if m.detail != nil || m.tab != tabDatabases {
		t.Errorf("tab key: detail=%v tab=%d", m.detail != nil, m.tab)
	}
}

func TestEnterOnEmptyTableIsNoop(t *testing.T) {
	m := press(loaded(t), "5") // users failed to load: no rows
	m = sendKey(m, tea.KeyEnter)
	if m.detail != nil {
		t.Error("detail opened on empty table")
	}
}

func TestDashboard(t *testing.T) {
	m := resize(loaded(t), 170, 40) // Overview is the default tab
	if m.tab != tabOverview {
		t.Fatalf("default tab = %d", m.tab)
	}
	out := m.render()
	for _, want := range []string{
		"0 Overview", "1 Projects", "1 running · 0 idle", "1 err", "running ×3", "error !",
		"Containers (2)", "Databases (1)",
		// container columns and values
		"Req/s", "5xx", "p95", "api", "50%", "6.0/s", "2%", "250ms",
		// database columns and values
		"Conn", "Lag", "WAL", "Cache", "pg-main", "30%", "71%", "14", "2.0s", "2.5M/s", "98%",
		"pg16",
		"Press T", "data (fr-par)", "Metrics need",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard missing %q:\n%s", want, out)
		}
	}
	if strings.ContainsAny(out, sparkTicks) {
		t.Errorf("overview tables must show numbers only, found a chart:\n%s", out)
	}
	for _, unwanted := range []string{"Spend", "Forecast", "EUR"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("overview shows %q:\n%s", unwanted, out)
		}
	}
	// Enter opens the container detail with its charts.
	m = sendKey(m, tea.KeyEnter)
	if out := m.render(); !strings.Contains(out, "Container api") || !strings.Contains(out, "max 50%") {
		t.Errorf("container detail without metrics:\n%s", out)
	}
}

func TestCreateTokensOnlyOnKeyPress(t *testing.T) {
	var created []core.ProjectRegion
	m := loaded(t)
	m.svc = fakeFetcher{createdTokens: &created}

	cmd, _ := m.handleKey("T")
	if cmd == nil {
		t.Fatal("T returned no command")
	}
	msg := cmd()
	if want := []core.ProjectRegion{{ProjectID: "p2", Region: "fr-par"}}; !reflect.DeepEqual(created, want) {
		t.Errorf("created %v, want %v", created, want)
	}
	tm, follow := m.Update(msg)
	if tm.(Model).tokens.err != nil || follow == nil {
		t.Errorf("after creation: err=%v follow=%v", tm.(Model).tokens.err, follow != nil)
	}
}

func TestMetricsFetchWaitsForBothLists(t *testing.T) {
	m := loaded(t)
	m.containers.loading, m.databases.loading = true, true
	if m.fetchMetricsWhenReady() != nil {
		t.Error("metrics fetched while lists are loading")
	}
	m.containers.loading, m.databases.loading = false, false
	cmd := m.fetchMetricsWhenReady()
	if cmd == nil {
		t.Fatal("metrics not fetched once both lists arrived")
	}
	if _, ok := cmd().(fetched[core.Metrics]); !ok {
		t.Error("metrics fetch returned wrong message")
	}
	m.lastRefresh[tabOverview] = m.now().Add(-2 * time.Minute)
	tm, cmd := m.Update(autoTickMsg{})
	if cmd == nil || !tm.(Model).containers.loading || !tm.(Model).databases.loading {
		t.Error("auto tick did not refresh containers and databases")
	}
}

func TestSparkline(t *testing.T) {
	if got := sparkline([]float64{0, 50, 100}, 3, core.UnitPercent); got != "▁▅█" {
		t.Errorf("percent sparkline = %q", got)
	}
	if got := sparkline([]float64{1, 2, 3, 4}, 2, core.UnitCount); got != "▄█" {
		t.Errorf("downsampled sparkline = %q", got)
	}
}

func TestOverviewCursorCrossesPanes(t *testing.T) {
	m := loaded(t) // containers: api, worker; databases: pg-main
	m = sendKey(m, tea.KeyDown)
	m = sendKey(m, tea.KeyDown) // past the last container
	if m.focus != 1 || m.cur().table.Cursor() != 0 {
		t.Fatalf("focus=%d cursor=%d, want databases pane row 0", m.focus, m.cur().table.Cursor())
	}
	m = sendKey(m, tea.KeyEnter)
	if out := m.render(); !strings.Contains(out, "Database pg-main") || !strings.Contains(out, "Replication lag") {
		t.Errorf("database detail wrong:\n%s", out)
	}
	m = sendKey(m, tea.KeyEscape)
	m = sendKey(m, tea.KeyUp) // back into the containers pane, last row
	if m.focus != 0 || m.cur().table.Cursor() != 1 {
		t.Errorf("focus=%d cursor=%d, want containers pane row 1", m.focus, m.cur().table.Cursor())
	}
}

func TestPaneHeightsFit(t *testing.T) {
	m := resize(loaded(t), 120, 30)
	if got := lipgloss.Height(m.render()); got > 30 {
		t.Errorf("render is %d lines, want <= 30", got)
	}
	m = resize(m, 60, 24)
	if got := lipgloss.Height(m.render()); got > 24 {
		t.Errorf("narrow render is %d lines, want <= 24", got)
	}
}

func TestShortEngine(t *testing.T) {
	for in, want := range map[string]string{"PostgreSQL-16": "pg16", "MySQL-8": "mysql8", "Redis": "redis"} {
		if got := shortEngine(in); got != want {
			t.Errorf("shortEngine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContainerRunningState(t *testing.T) {
	m := loaded(t)
	c := m.containers.data[0] // api: 3 instances in the fake metrics
	if got := m.containerState(c); got != "running ×3" {
		t.Errorf("state = %q, want running ×3", got)
	}

	// Scaled to zero: the instances query reports 0.
	m.metrics.data.ByResource["c1"][core.MetricInstances] = series(core.UnitCount, 2, 0)
	if got := m.containerState(c); got != "idle" {
		t.Errorf("state = %q, want idle", got)
	}

	// Unknown instance count (e.g. no Cockpit token): fall back to API status.
	delete(m.metrics.data.ByResource, "c1")
	if got := m.containerState(c); got != "ready" {
		t.Errorf("state = %q, want ready", got)
	}

	// The detail view explains it.
	m = loaded(t)
	m = sendKey(m, tea.KeyEnter)
	if out := m.render(); !strings.Contains(out, "3 instance(s)") || !strings.Contains(out, "scale 0–0") {
		t.Errorf("detail missing running info:\n%s", out)
	}
}

func TestMetricColumnsWithoutDataAreHidden(t *testing.T) {
	m := resize(loaded(t), 170, 40)
	out := m.render()
	// The fake databases report no transactions metric; containers do report latency.
	if strings.Contains(out, " TPS ") {
		t.Errorf("empty TPS column shown:\n%s", out)
	}
	if !strings.Contains(out, "p95") {
		t.Errorf("p95 column with data hidden:\n%s", out)
	}
	// Before any metrics arrive, all columns are kept.
	m.metrics.data.ByResource = nil
	m.rebuildTable()
	if out := m.render(); !strings.Contains(out, "TPS") {
		t.Errorf("columns hidden before metrics loaded:\n%s", out)
	}
}

func TestDatabaseConnectionsAsText(t *testing.T) {
	m := resize(loaded(t), 170, 40)
	if !strings.Contains(m.render(), "14/100") {
		t.Errorf("table missing 14/100:\n%s", m.render())
	}
	rm := m.metrics.data.ByResource["d1"]
	for _, mc := range databaseMetricCols {
		if mc.kind == core.MetricConnections {
			if cell := metricCell(rm, mc); cell != "14/100" || strings.ContainsAny(cell, sparkTicks) {
				t.Errorf("connections cell = %q, want plain 14/100", cell)
			}
		}
	}
	// Without the limit only the count is shown.
	delete(rm, core.MetricMaxConnections)
	if s, _ := connectionsText(rm); s != "14" {
		t.Errorf("without max: %q", s)
	}

	// The detail shows the same text, no chart.
	m = resize(loaded(t), 120, 40)
	m = sendKey(m, tea.KeyDown)
	m = sendKey(m, tea.KeyDown) // into the databases pane
	m = sendKey(m, tea.KeyEnter)
	out := m.render()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Connections") && (!strings.Contains(line, "14/100") || strings.ContainsAny(line, sparkTicks)) {
			t.Errorf("detail connections line = %q", line)
		}
	}
}

func TestNarrowOverviewKeepsShortValuesWhole(t *testing.T) {
	m := resize(loaded(t), 60, 26)
	out := m.render()
	for _, want := range []string{"running ×3", "14/100", "error !"} {
		if !strings.Contains(out, want) {
			t.Errorf("narrow overview cut %q:\n%s", want, out)
		}
	}
}

func TestLayoutShrinksFlexibleColumnsFirst(t *testing.T) {
	cols := []col{{"Name", 10, 2}, {"Status", 10, 0}, {"CPU", 5, 1}}
	got := layout(cols, 2*3+20) // 5 short of the 25 minimum
	if got[1].Width != 10 {
		t.Errorf("fixed Status column shrunk to %d", got[1].Width)
	}
	if got[0].Width+got[1].Width+got[2].Width != 20 {
		t.Errorf("widths %v do not fit", got)
	}
}

func TestAutoRefreshPerTab(t *testing.T) {
	m := loaded(t)
	if !m.auto[tabOverview] {
		t.Fatal("Overview auto-refresh should start on")
	}
	for _, tb := range []tab{tabProjects, tabContainers, tabDatabases, tabStorage, tabUsers, tabBilling} {
		if m.auto[tb] {
			t.Errorf("tab %d auto-refresh on by default", tb)
		}
	}
	if !strings.Contains(m.render(), "auto 1m") {
		t.Errorf("overview does not show auto state:\n%s", m.render())
	}

	// Users: off, so a due tick does nothing.
	m = press(m, "5")
	m.lastRefresh[tabUsers] = m.now().Add(-time.Hour)
	m.onAutoTick()
	if m.users.loading {
		t.Error("users refreshed with auto-refresh off")
	}
	if !strings.Contains(m.render(), "auto off") {
		t.Errorf("users tab does not show auto off:\n%s", m.render())
	}

	// Toggling on refreshes immediately, and only users.
	cmd, _ := m.handleKey("a")
	if cmd == nil || !m.auto[tabUsers] || !m.users.loading {
		t.Fatalf("toggle on: cmd=%v auto=%v loading=%v", cmd != nil, m.auto[tabUsers], m.users.loading)
	}
	if m.billing.loading || m.containers.loading {
		t.Error("toggling users refreshed other data")
	}
	if !strings.Contains(m.render(), "auto 5m") {
		t.Errorf("users tab does not show auto 5m:\n%s", m.render())
	}

	// Not due yet: nothing happens; due: users refresh again.
	m.users.loading = false
	m.lastRefresh[tabUsers] = m.now().Add(-4 * time.Minute)
	if m.onAutoTick(); m.users.loading {
		t.Error("refreshed before the 5m interval")
	}
	m.lastRefresh[tabUsers] = m.now().Add(-6 * time.Minute)
	if m.onAutoTick(); !m.users.loading {
		t.Error("not refreshed after the 5m interval")
	}

	// Toggling off.
	m.handleKey("a")
	if m.auto[tabUsers] || !m.auto[tabOverview] {
		t.Errorf("toggle off affected wrong tabs: users=%v overview=%v", m.auto[tabUsers], m.auto[tabOverview])
	}
}

func TestAutoRefreshOnlyVisibleTab(t *testing.T) {
	m := press(loaded(t), "6") // billing, auto off; overview still on
	m.lastRefresh[tabOverview] = m.now().Add(-time.Hour)
	m.onAutoTick()
	if m.containers.loading || m.databases.loading {
		t.Error("hidden Overview refreshed while viewing Billing")
	}
}

func TestAutoToggleInDetail(t *testing.T) {
	m := press(loaded(t), "2")
	m = sendKey(m, tea.KeyEnter)
	tm, _ := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = tm.(Model)
	if !m.auto[tabContainers] || m.detail == nil {
		t.Errorf("a in detail: auto=%v detail open=%v", m.auto[tabContainers], m.detail != nil)
	}
}

func TestStatusBar(t *testing.T) {
	for _, width := range []int{120, 60} {
		m := resize(loaded(t), width, 30)
		lines := strings.Split(m.render(), "\n")
		bar := lines[len(lines)-1]
		if lipgloss.Width(bar) != width || !strings.HasSuffix(strings.TrimRight(ansiStrip(bar), " "), "q quit") {
			t.Errorf("width %d: bar %q (width %d) should end with q quit at the edge", width, ansiStrip(bar), lipgloss.Width(bar))
		}
		if !strings.Contains(bar, "a auto 1m") {
			t.Errorf("width %d: bar lacks auto state: %q", width, ansiStrip(bar))
		}
		if strings.Contains(lines[1], "auto") {
			t.Errorf("width %d: auto state still under the tabs: %q", width, ansiStrip(lines[1]))
		}
	}
	// Detail view uses the same bar.
	m := resize(loaded(t), 100, 30)
	m = sendKey(m, tea.KeyEnter)
	lines := strings.Split(m.render(), "\n")
	if bar := ansiStrip(lines[len(lines)-1]); !strings.HasPrefix(bar, "esc back") || !strings.HasSuffix(bar, "q quit") {
		t.Errorf("detail bar = %q", bar)
	}
}

func ansiStrip(s string) string { return ansi.Strip(s) }

func TestDetailLinks(t *testing.T) {
	var opened []string
	defer func(orig func(string) error) { opener = orig }(opener)
	opener = func(url string) error { opened = append(opened, url); return nil }

	m := resize(loaded(t), 120, 60)
	m = sendKey(m, tea.KeyEnter) // container api
	out := m.render()
	if !strings.Contains(out, "Cockpit (Grafana)") {
		t.Fatalf("no links section:\n%s", ansi.Strip(out))
	}
	// OSC 8 hyperlink to the containers dashboard, filtered by Cockpit's name.
	if !strings.Contains(out, "\x1b]8;;https://p1.dashboard.example/d/scw-serverless-containers-overview/") ||
		!strings.Contains(out, "var-container_name=nsabc123-api") {
		t.Errorf("no clickable metrics link in:\n%q", out)
	}

	tm, _ := m.Update(tea.KeyPressMsg{Code: 'm', Text: "m"})
	tm, _ = tm.(Model).Update(tea.KeyPressMsg{Code: 'L', Text: "L"})
	m = tm.(Model)
	if len(opened) != 2 || !strings.Contains(opened[0], "serverless-containers-overview") || !strings.Contains(opened[1], "serverless-containers-logs") {
		t.Errorf("opened %v", opened)
	}
	if m.detail == nil {
		t.Error("opening a link closed the detail")
	}

	// A failing opener is reported, not fatal.
	opener = func(string) error { return errors.New("no browser") }
	tm, _ = m.Update(tea.KeyPressMsg{Code: 'm', Text: "m"})
	if out := ansi.Strip(tm.(Model).render()); !strings.Contains(out, "no browser") {
		t.Errorf("open error not shown:\n%s", out)
	}
}

func TestDatabaseDetailLinks(t *testing.T) {
	m := resize(loaded(t), 120, 60)
	m = sendKey(m, tea.KeyDown)
	m = sendKey(m, tea.KeyDown) // databases pane
	m = sendKey(m, tea.KeyEnter)
	out := m.render()
	if !strings.Contains(out, "https://p2.dashboard.example/d/scw-rdb-postgresql-overview/") ||
		!strings.Contains(out, "var-resource_name=pg-main") {
		t.Errorf("no database dashboard link in:\n%q", out)
	}

	// The full URL is shown as text; wrapped, every line links the whole URL.
	m = resize(m, 80, 60)
	out = m.render()
	logs := m.links(m.detail.item).Logs
	if !strings.Contains(ansi.Strip(out), "https://p2.dashboard.example/explore?left=") {
		t.Errorf("logs URL not shown:\n%s", ansi.Strip(out))
	}
	if n := strings.Count(out, "\x1b]8;;"+logs+"\a"); n < 2 {
		t.Errorf("wrapped logs URL has %d linked lines, want several", n)
	}
}

func TestOverviewTableLinks(t *testing.T) {
	var opened []string
	defer func(orig func(string) error) { opener = orig }(opener)
	opener = func(url string) error { opened = append(opened, url); return nil }

	m := resize(loaded(t), 160, 40)
	out := m.render()
	// Container name links to its metrics dashboard, Logs column to its logs.
	if !strings.Contains(out, "\x1b]8;;https://p1.dashboard.example/d/scw-serverless-containers-overview/") {
		t.Errorf("container name is not a metrics link:\n%q", out)
	}
	if !strings.Contains(out, "\x1b]8;;https://p1.dashboard.example/d/scw-serverless-containers-logs/") {
		t.Errorf("no container logs link")
	}
	if !strings.Contains(out, "\x1b]8;;https://p2.dashboard.example/d/scw-rdb-postgresql-overview/") {
		t.Errorf("database name is not a metrics link")
	}
	if !strings.Contains(ansi.Strip(out), "metrics ↗") || !strings.Contains(ansi.Strip(out), "logs ↗") ||
		!strings.Contains(ansi.Strip(out), "m metrics · L logs") {
		t.Errorf("link columns or key hints missing:\n%s", ansi.Strip(out))
	}

	// m/L open the selected row's links without a detail.
	m = sendKey(m, tea.KeyDown) // worker (p2): links still exist, unfiltered
	m = sendKey(m, tea.KeyDown) // pg-main
	tm, _ := m.Update(tea.KeyPressMsg{Code: 'm', Text: "m"})
	tm, _ = tm.(Model).Update(tea.KeyPressMsg{Code: 'L', Text: "L"})
	if len(opened) != 2 || !strings.Contains(opened[0], "scw-rdb-postgresql-overview") || !strings.Contains(opened[1], "/explore?") {
		t.Errorf("opened %v", opened)
	}

	// Narrow screens drop the link columns but keep the name links.
	m = resize(m, 60, 30)
	if out := m.render(); strings.Contains(ansi.Strip(out), "metrics ↗") || strings.Contains(ansi.Strip(out), "logs ↗") || !strings.Contains(out, "\x1b]8;;") {
		t.Errorf("narrow layout wrong:\n%s", ansi.Strip(out))
	}

	// m on a tab without links is not handled.
	m = press(m, "4")
	if _, handled := m.handleKey("m"); handled {
		t.Error("m handled on the Users tab")
	}
}

// A link cell must not reset all attributes, or it cuts off the
// highlight of the selected row after the first link.
func TestLinkCellKeepsHighlight(t *testing.T) {
	row := tableStyles(true).Selected.Render("a " + linkCell("name", "https://example.com") + " b")
	if strings.Count(row, "\x1b[m") != 1 || !strings.HasSuffix(row, "\x1b[m") {
		t.Errorf("highlight reset inside the row: %q", row)
	}
}

func TestStorageTab(t *testing.T) {
	m := resize(press(loaded(t), "4"), 120, 40)
	out := ansi.Strip(m.render())
	for _, want := range []string{
		"4 Storage", "Object 1 · Registry 1 (1.5G) · Block 1 (50.0G) · Local 1 (20.0G) · File 0",
		"Object Storage buckets (1)", "assets", "https://assets.s3.nl-ams",
		"Container Registry namespaces (1)", "images", "private",
		"Block Storage volumes (1)", "pgdata", "sbs_5k", "db-vm",
		"Local volumes (1)", "l_ssd",
		"permission denied", // the failed product is reported, the rest still shown
	} {
		if !strings.Contains(out, want) {
			t.Errorf("storage tab missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "File Storage (") {
		t.Errorf("empty File Storage pane shown:\n%s", out)
	}

	// Scoped to "web": only its bucket and registry; one pane still titled.
	m = press(m, "p")
	out = ansi.Strip(m.render())
	if strings.Contains(out, "pgdata") || !strings.Contains(out, "Object Storage buckets (1)") {
		t.Errorf("scoped storage wrong:\n%s", out)
	}

	// Detail of the block volume, reached across panes.
	m = press(m, "P")
	m = sendKey(m, tea.KeyDown)
	m = sendKey(m, tea.KeyDown)
	m = sendKey(m, tea.KeyEnter)
	out = ansi.Strip(m.render())
	for _, want := range []string{"Block Storage volumes: pgdata", "5000 IOPS", "Attached to", "db-vm", "50.0G"} {
		if !strings.Contains(out, want) {
			t.Errorf("volume detail missing %q:\n%s", want, out)
		}
	}
}

func TestProjectDetailListsStorage(t *testing.T) {
	m := press(loaded(t), "1")
	m = sendKey(m, tea.KeyEnter) // web
	if out := ansi.Strip(m.render()); !strings.Contains(out, "Object  assets  nl-ams") ||
		!strings.Contains(out, "Registry  images  fr-par  1.5G") {
		t.Errorf("project detail storage missing:\n%s", out)
	}
}

// Each tab shows when its own data last arrived, so an auto-refresh of the
// Overview moves its "updated" time without touching other tabs.
func TestUpdatedTimePerTab(t *testing.T) {
	m := loaded(t) // everything delivered at 12:00:00
	if out := ansi.Strip(m.render()); !strings.Contains(out, "updated 12:00:00") {
		t.Fatalf("no initial updated time:\n%s", out)
	}
	m.now = func() time.Time { return time.Date(2026, 10, 11, 12, 1, 30, 0, time.UTC) }
	c, _ := fakeFetcher{}.Containers(context.Background())
	tm, _ := m.Update(fetched[[]core.Container]{data: c})
	m = tm.(Model)
	if out := ansi.Strip(m.render()); !strings.Contains(out, "updated 12:01:30") {
		t.Errorf("overview updated time not moved:\n%s", out)
	}
	if out := ansi.Strip(press(m, "5").render()); !strings.Contains(out, "updated 12:00:00") {
		t.Errorf("users updated time changed by a container refresh:\n%s", out)
	}
}
