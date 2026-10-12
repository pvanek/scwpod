package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/pvanek/scwpod/internal/core"
)

// Auto-refresh is toggled per tab with "a". Only the visible tab refreshes,
// and only the data it shows; open detail views follow along.

// autoIntervals is how often each tab refreshes when auto-refresh is on.
var autoIntervals = [tabCount]time.Duration{
	tabOverview:   time.Minute,
	tabProjects:   time.Minute,
	tabContainers: time.Minute,
	tabDatabases:  time.Minute,
	tabStorage:    5 * time.Minute,
	tabUsers:      5 * time.Minute,
	tabBilling:    5 * time.Minute, // billing data itself updates only hourly
}

// autoTickEvery is how often due refreshes are checked.
const autoTickEvery = 5 * time.Second

type autoTickMsg struct{}

func autoTick() tea.Cmd {
	return tea.Tick(autoTickEvery, func(time.Time) tea.Msg { return autoTickMsg{} })
}

// onAutoTick refreshes the visible tab if its auto-refresh is on and due.
func (m *Model) onAutoTick() tea.Cmd {
	t := m.tab
	if !m.auto[t] || m.now().Sub(m.lastRefresh[t]) < autoIntervals[t] {
		return nil
	}
	return m.refreshTab(t)
}

// toggleAuto switches auto-refresh of the visible tab; switching it on
// refreshes right away.
func (m *Model) toggleAuto() tea.Cmd {
	m.auto[m.tab] = !m.auto[m.tab]
	if m.auto[m.tab] {
		return m.refreshTab(m.tab)
	}
	return nil
}

// refreshTab fetches the data shown on tab t. Container and database
// refreshes also refresh their metrics once both lists have arrived.
func (m *Model) refreshTab(t tab) tea.Cmd {
	m.lastRefresh[t] = m.now()
	var cmds []tea.Cmd
	containers := func() {
		m.containers.loading = true
		cmds = append(cmds, fetch(m.svc.Containers))
	}
	databases := func() {
		m.databases.loading = true
		cmds = append(cmds, fetch(m.svc.Databases))
	}
	billing := func() {
		m.billing.loading = true
		now := m.now()
		period := core.CurrentPeriod(now)
		cmds = append(cmds, fetch(func(ctx context.Context) (core.Billing, error) { return m.svc.Billing(ctx, period, now) }))
	}

	switch t {
	case tabOverview, tabContainers, tabDatabases:
		// Metrics need both lists, so both are refreshed.
		containers()
		databases()
	case tabProjects:
		m.projects.loading = true
		cmds = append(cmds, fetch(m.svc.Projects))
		containers()
		databases()
		billing()
	case tabStorage:
		m.storage.loading = true
		cmds = append(cmds, fetch(m.svc.Storage), m.fetchBuckets())
	case tabUsers:
		m.users.loading = true
		cmds = append(cmds, fetch(m.svc.Users))
	case tabBilling:
		billing()
	}
	return tea.Batch(cmds...)
}

// autoLabel describes the visible tab's auto-refresh state.
func (m Model) autoLabel() string {
	if m.auto[m.tab] {
		return "a auto " + fmtDuration(autoIntervals[m.tab])
	}
	return "a auto off"
}
