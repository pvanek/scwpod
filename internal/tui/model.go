// Package tui is the Bubble Tea terminal front end over internal/core.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/pvanek/scwpod/internal/core"
)

// Fetcher is the subset of core.Service the UI needs (fakeable in tests).
type Fetcher interface {
	Projects(ctx context.Context) ([]core.Project, error)
	Containers(ctx context.Context) ([]core.Container, error)
	Databases(ctx context.Context) ([]core.Database, error)
	Instances(ctx context.Context) ([]core.Instance, error)
	Storage(ctx context.Context) ([]core.StorageItem, error)
	Buckets(ctx context.Context, projectIDs []string) ([]core.StorageItem, error)
	Users(ctx context.Context) ([]core.User, error)
	Billing(ctx context.Context, period string, now time.Time) (core.Billing, error)
	Metrics(ctx context.Context, containers []core.Container, dbs []core.Database, instances []core.Instance, now time.Time) (core.Metrics, error)
	GrafanaURLs(ctx context.Context, projectIDs []string) (map[string]string, error)
	// CreateCockpitTokens creates read-only metrics tokens; it changes the account.
	CreateCockpitTokens(ctx context.Context, scopes []core.ProjectRegion) error
}

const fetchTimeout = 30 * time.Second

type tab int

const (
	tabOverview tab = iota
	tabProjects
	tabInstances
	tabContainers
	tabDatabases
	tabStorage
	tabUsers
	tabBilling
	tabCount
)

var tabNames = [tabCount]string{"Overview", "Projects", "Instances", "Containers", "Databases", "Storage", "Users", "Billing"}

// resource is the load state of one data set.
type resource[T any] struct {
	loading bool
	data    T
	err     error
}

// fetched carries the result of one fetch back into Update.
type fetched[T any] struct {
	data T
	err  error
}

type Model struct {
	svc Fetcher
	now func() time.Time

	width, height int
	tab           tab
	scope         int // 0 = all projects, i = projects[i-1]

	projects   resource[[]core.Project]
	containers resource[[]core.Container]
	databases  resource[[]core.Database]
	instances  resource[[]core.Instance]
	storage    resource[[]core.StorageItem] // all but buckets
	buckets    resource[bucketList]
	users      resource[[]core.User]
	billing    resource[core.Billing]
	metrics    resource[core.Metrics]
	tokens     resource[tokensCreated]
	grafana    resource[map[string]string] // Grafana URL per project ID

	auto        [tabCount]bool      // auto-refresh per tab
	lastRefresh [tabCount]time.Time // when each tab's data was last requested
	updated     [tabCount]time.Time // when data shown on each tab last arrived

	panes   []pane
	focus   int // index of the focused pane
	detail  *detailView
	help    *helpView // over everything else when open
	openErr error     // last failure to open a link in the browser
	spinner spinner.Model
}

func New(svc Fetcher) Model {
	m := Model{
		svc:     svc,
		now:     time.Now,
		spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
	m.auto[tabOverview] = true
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.refresh(), autoTick())
}

// tokensCreated is the result of creating Cockpit tokens.
type tokensCreated struct{}

// refresh marks everything as loading and fetches all data concurrently.
func (m *Model) refresh() tea.Cmd {
	m.projects.loading = true
	m.containers.loading = true
	m.databases.loading = true
	m.instances.loading = true
	m.storage.loading = true
	m.users.loading = true
	m.billing.loading = true
	now := m.now()
	period := core.CurrentPeriod(now)
	for t := range m.lastRefresh {
		m.lastRefresh[t] = now
	}
	return tea.Batch(
		fetch(m.svc.Projects),
		fetch(m.svc.Containers),
		fetch(m.svc.Databases),
		fetch(m.svc.Instances),
		fetch(m.svc.Storage),
		fetch(m.svc.Users),
		fetch(func(ctx context.Context) (core.Billing, error) { return m.svc.Billing(ctx, period, now) }),
	)
}

func fetch[T any](fn func(context.Context) (T, error)) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		data, err := fn(ctx)
		return fetched[T]{data: data, err: err}
	}
}

func set[T any](r *resource[T], msg fetched[T]) {
	*r = resource[T]{data: msg.data, err: msg.err}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var follow tea.Cmd // fetches chained on the incoming data
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		if m.help != nil {
			return m, m.handleHelpKey(msg)
		}
		if m.detail != nil {
			return m, m.handleDetailKey(msg)
		}
		if cmd, handled := m.handleKey(msg.String()); handled {
			m.rebuildTable()
			return m, cmd
		}
		// Navigation keys belong to the tables; nothing to rebuild.
		return m, m.navigate(msg)
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case fetched[map[string]string]:
		set(&m.grafana, msg)
	case fetched[[]core.Project]:
		set(&m.projects, msg)
		m.markUpdated(tabProjects)
		if msg.err == nil {
			follow = tea.Batch(m.fetchGrafanaURLs(), m.fetchBuckets())
		}
		if m.scope > len(m.projects.data) {
			m.scope = 0
		}
	case fetched[[]core.Container]:
		set(&m.containers, msg)
		m.markUpdated(tabOverview, tabProjects, tabContainers)
		follow = m.fetchMetricsWhenReady()
	case fetched[[]core.Database]:
		set(&m.databases, msg)
		m.markUpdated(tabOverview, tabProjects, tabDatabases)
		follow = m.fetchMetricsWhenReady()
	case fetched[[]core.Instance]:
		set(&m.instances, msg)
		m.markUpdated(tabOverview, tabProjects, tabInstances)
		follow = m.fetchMetricsWhenReady()
	case fetched[core.Metrics]:
		set(&m.metrics, msg)
		m.markUpdated(tabOverview, tabInstances, tabContainers, tabDatabases)
	case fetched[tokensCreated]:
		set(&m.tokens, msg)
		if msg.err == nil {
			follow = m.fetchMetrics()
		}
	case autoTickMsg:
		follow = tea.Batch(m.onAutoTick(), autoTick())
	case fetched[[]core.StorageItem]:
		set(&m.storage, msg)
		m.markUpdated(tabStorage)
	case fetched[bucketList]:
		set(&m.buckets, msg)
		m.markUpdated(tabStorage)
	case fetched[[]core.User]:
		set(&m.users, msg)
		m.markUpdated(tabUsers)
	case fetched[core.Billing]:
		set(&m.billing, msg)
		m.markUpdated(tabProjects, tabBilling)
	default:
		return m, nil
	}
	m.rebuildTable()
	return m, follow
}

// markUpdated records that data shown on the given tabs just arrived.
func (m *Model) markUpdated(tabs ...tab) {
	now := m.now()
	for _, t := range tabs {
		m.updated[t] = now
	}
}

func (m *Model) handleKey(k string) (tea.Cmd, bool) {
	switch k {
	case "q", "ctrl+c":
		return tea.Quit, true
	case "tab", "right":
		m.setTab((m.tab + 1) % tabCount)
	case "shift+tab", "left", "h":
		m.setTab((m.tab + tabCount - 1) % tabCount)
	case "0", "1", "2", "3", "4", "5", "6", "7":
		m.setTab(tab(k[0] - '0'))
	case "p":
		m.scope = (m.scope + 1) % (len(m.projects.data) + 1)
		m.resetCursors()
	case "P":
		n := len(m.projects.data) + 1
		m.scope = (m.scope + n - 1) % n
		m.resetCursors()
	case "r":
		return m.refresh(), true
	case "enter":
		m.openDetail()
	case "T":
		return m.createTokens(), true
	case "f1", "?":
		m.openHelp()
	case "a":
		return m.toggleAuto(), true
	case "m", "l", "L":
		switch m.tab {
		case tabOverview, tabContainers, tabDatabases:
			if k == "m" {
				m.openLink("metrics")
			} else {
				m.openLink("logs")
			}
		default:
			return nil, false
		}
	default:
		return nil, false
	}
	return nil, true
}

func (m *Model) setTab(t tab) {
	m.tab = t
	m.openErr = nil
	m.panes = nil // recreated by rebuildTable with fresh cursors
}

// scopeID returns the selected project ID or core.AllProjects.
func (m Model) scopeID() string {
	if m.scope == 0 || m.scope > len(m.projects.data) {
		return core.AllProjects
	}
	return m.projects.data[m.scope-1].ID
}

func (m Model) scopeName() string {
	if id := m.scopeID(); id != core.AllProjects {
		return m.projects.data[m.scope-1].Name
	}
	return fmt.Sprintf("All projects (%d)", len(m.projects.data))
}

func (m Model) projectName(id string) string {
	for _, p := range m.projects.data {
		if p.ID == id {
			return p.Name
		}
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// currentState reports loading/error for the active tab.
func (m Model) currentState() (loading bool, err error) {
	switch m.tab {
	case tabOverview:
		return m.containers.loading || m.databases.loading || m.instances.loading || m.metrics.loading || m.tokens.loading,
			firstErr(m.containers.err, m.databases.err, m.instances.err, m.tokens.err, m.metrics.err)
	case tabProjects:
		return m.containers.loading || m.databases.loading || m.instances.loading || m.billing.loading,
			firstErr(m.projects.err, m.containers.err, m.databases.err, m.instances.err, m.billing.err)
	case tabInstances:
		return m.instances.loading || m.metrics.loading, firstErr(m.instances.err, m.metrics.err)
	case tabContainers:
		return m.containers.loading, m.containers.err
	case tabDatabases:
		return m.databases.loading, m.databases.err
	case tabStorage:
		return m.storage.loading || m.buckets.loading, errors.Join(m.storage.err, m.buckets.err)
	case tabUsers:
		return m.users.loading, m.users.err
	case tabBilling:
		return m.billing.loading, m.billing.err
	}
	return false, nil
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

var (
	activeTab   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("57")).Padding(0, 1)
	inactiveTab = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Padding(0, 1)
	dim         = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	scopeStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86"))
)

func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "scwpod"
	return v
}

func (m Model) render() string {
	if m.help != nil {
		return m.renderHelp()
	}
	if m.detail != nil {
		return m.renderDetail()
	}
	return m.header() + "\n" + m.panesView() + "\n" +
		m.statusBar(m.listHints())
}

// listHints are the key hints for table screens.
func (m Model) listHints() string {
	h := "F1 help · ←/→ tab · p/P project · ↑/↓ move · enter detail"
	switch m.tab {
	case tabOverview, tabContainers, tabDatabases:
		h += " · m open metrics · l open logs"
	}
	return h + " · r refresh"
}

// statusBar renders the bottom line: the given key hints, the auto-refresh
// state and last update, and "q quit" at the right edge. Hints are cut
// first when the screen is narrow.
func (m Model) statusBar(hints string) string {
	const right = "q quit"
	width := max(m.width, 20)
	state := m.autoLabel()
	if t := m.updated[m.tab]; !t.IsZero() {
		if updated := state + " · updated " + t.Format("15:04:05"); lipgloss.Width(updated)+len(right)+8 <= width {
			state = updated
		}
	}
	room := width - lipgloss.Width(state) - len(right) - 4 // " · " and a space before right
	left := state
	if room >= 4 {
		left = truncate(hints, room) + " · " + state
	}
	gap := max(width-lipgloss.Width(left)-len(right), 1)
	return dim.Render(left + strings.Repeat(" ", gap) + right)
}

// header is everything above the table: tabs, scope, summary and errors.
func (m Model) header() string {
	var b strings.Builder
	b.WriteString(m.tabsLine() + "\n" + m.scopeLine())

	switch m.tab {
	case tabOverview:
		b.WriteString("\n" + m.dashboardHeader())
	case tabProjects:
		b.WriteString("\n" + m.spendLine())
	case tabInstances:
		b.WriteString("\n" + m.instancesLine())
	case tabStorage:
		b.WriteString("\n" + m.storageLine())
	case tabBilling:
		b.WriteString("\n" + m.billingPanel())
	}
	if _, err := m.currentState(); err != nil {
		b.WriteString("\n" + errStyle.Width(max(m.width, 20)).Render("⚠ "+err.Error()))
	}
	if m.openErr != nil {
		b.WriteString("\n" + errStyle.Width(max(m.width, 20)).Render("⚠ "+m.openErr.Error()))
	}
	return b.String()
}

// tabsLine renders the tab bar; on narrow screens inactive tabs show only
// their number.
func (m Model) tabsLine() string {
	render := func(short bool) string {
		tabs := make([]string, tabCount)
		for i, name := range tabNames {
			style, label := inactiveTab, fmt.Sprintf("%d %s", i, name)
			if tab(i) == m.tab {
				style = activeTab
			} else if short {
				label = fmt.Sprint(i)
			}
			tabs[i] = style.Render(label)
		}
		return lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
	}
	if line := render(false); lipgloss.Width(line) <= m.width {
		return line
	}
	return render(true)
}

func (m Model) scopeLine() string {
	scope := "Project: " + scopeStyle.Render(m.scopeName())
	if m.tab == tabUsers {
		scope = dim.Render("Users are organization-wide")
	}
	if loading, _ := m.currentState(); loading {
		scope += "  " + m.spinner.View() + dim.Render(" loading…")
	}
	return scope
}

// spendLine is the one-line month-to-date spend headline for the scope.
func (m Model) spendLine() string {
	total, cur := m.scopedSpend().Total()
	s := fmt.Sprintf("Spend %s: %s", m.billing.data.Period, money(total, cur))
	if !m.billing.data.UpdatedAt.IsZero() {
		s += dim.Render(" (billing data as of " + m.billing.data.UpdatedAt.Local().Format("2006-01-02 15:04") + ")")
	}
	return s
}

// scopedSpend is the current period's consumption limited to the scope.
func (m Model) scopedSpend() core.Spend {
	sp := m.billing.data.Spend
	sp.Lines = core.ByProject(sp.Lines, m.scopeID(), func(l core.SpendLine) string { return l.ProjectID })
	return sp
}

func money(v float64, currency string) string {
	if currency == "" {
		currency = "EUR"
	}
	return fmt.Sprintf("%.2f %s", v, currency)
}
