package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/pvanek/scwpod/internal/core"
)

// col is a column with a minimum width and a share of any leftover width.
type col struct {
	title  string
	min    int
	weight int
}

// pane is one table on screen. Most tabs have one; the Overview has one per
// resource type.
type pane struct {
	title string
	table table.Model
	items []any // item behind each row
}

// paneData is what a tab produces for one pane.
type paneData struct {
	title string
	cols  []col
	rows  []table.Row
	items []any
}

func newTable() table.Model {
	t := table.New(table.WithFocused(true))
	t.SetStyles(tableStyles(true))
	return t
}

func tableStyles(focused bool) table.Styles {
	s := table.DefaultStyles()
	s.Header = s.Header.Bold(true).BorderStyle(lipgloss.NormalBorder()).BorderBottom(true)
	if focused {
		s.Selected = s.Selected.Foreground(lipgloss.Color("230")).Background(lipgloss.Color("57"))
	} else {
		// The row is already padded per cell; any padding here would shift it.
		s.Selected = lipgloss.NewStyle()
	}
	return s
}

// cur returns the focused pane, or nil before the first rebuild. The pane
// lives in the shared panes slice, so changes through it persist.
func (m Model) cur() *pane {
	if m.focus < 0 || m.focus >= len(m.panes) {
		return nil
	}
	return &m.panes[m.focus]
}

// rebuildTable recomputes columns and rows of every pane for the active tab
// and scope, keeping cursors, and splits the height between panes.
func (m *Model) rebuildTable() {
	data := m.tableData()
	if len(m.panes) != len(data) {
		m.panes = make([]pane, len(data))
		for i := range m.panes {
			m.panes[i].table = newTable()
		}
		m.focus = 0
	}

	avail := m.height - lipgloss.Height(m.header()) - 1 // footer
	if showPaneTitles(m.tab, len(data)) {
		avail -= len(data) // pane titles
	}
	heights := splitHeights(data, max(avail, 4*len(data)))

	for i, d := range data {
		p := &m.panes[i]
		p.title, p.items = d.title, d.items
		// Clear rows first: the table indexes rows by column, so a shorter
		// column set with old rows would render out of range. SetRows(nil)
		// resets the cursor, so restore it afterwards.
		cursor := p.table.Cursor()
		p.table.SetRows(nil)
		p.table.SetColumns(layout(d.cols, m.width))
		p.table.SetRows(d.rows)
		p.table.SetCursor(cursor)
		p.table.SetWidth(max(m.width, 20))
		p.table.SetHeight(heights[i])
		p.table.SetStyles(tableStyles(i == m.focus))
	}
	m.syncDetail()
}

// splitHeights gives each pane its rows plus header (2 lines) if everything
// fits, the last pane taking the rest; otherwise height is shared in
// proportion to the rows, at least 4 lines each.
func splitHeights(data []paneData, avail int) []int {
	out := make([]int, len(data))
	if len(data) == 1 {
		out[0] = avail
		return out
	}
	want, total := make([]int, len(data)), 0
	for i, d := range data {
		want[i] = max(len(d.rows), 1) + 2
		total += want[i]
	}
	if total <= avail {
		copy(out, want)
		out[len(out)-1] += avail - total
		return out
	}
	used := 0
	for i := range data {
		out[i] = max(4, want[i]*avail/total)
		used += out[i]
	}
	out[len(out)-1] = max(4, out[len(out)-1]+avail-used)
	return out
}

// navigate passes a key the app does not handle to the focused table. On the
// Overview, moving past the last row of a pane continues in the next one.
func (m *Model) navigate(msg tea.KeyPressMsg) tea.Cmd {
	p := m.cur()
	if p == nil {
		return nil
	}
	switch msg.String() {
	case "down", "j":
		if p.table.Cursor() >= len(p.items)-1 && m.focusNext(1) {
			return nil
		}
	case "up", "k":
		if p.table.Cursor() <= 0 && m.focusNext(-1) {
			return nil
		}
	}
	var cmd tea.Cmd
	p.table, cmd = p.table.Update(msg)
	return cmd
}

// focusNext moves focus to the next non-empty pane in direction dir,
// placing the cursor at its near end. It reports whether focus moved.
func (m *Model) focusNext(dir int) bool {
	for i := m.focus + dir; i >= 0 && i < len(m.panes); i += dir {
		if len(m.panes[i].items) == 0 {
			continue
		}
		m.panes[m.focus].table.SetStyles(tableStyles(false))
		m.focus = i
		p := &m.panes[i]
		if dir > 0 {
			p.table.SetCursor(0)
		} else {
			p.table.SetCursor(len(p.items) - 1)
		}
		p.table.SetStyles(tableStyles(true))
		return true
	}
	return false
}

func (m *Model) resetCursors() {
	for i := range m.panes {
		m.panes[i].table.SetCursor(0)
	}
}

func (m Model) panesView() string {
	var parts []string
	for i, p := range m.panes {
		if showPaneTitles(m.tab, len(m.panes)) {
			style := dim
			if i == m.focus {
				style = sectionStyle
			}
			parts = append(parts, style.Render(fmt.Sprintf("%s (%d)", p.title, len(p.items))))
		}
		parts = append(parts, p.table.View())
	}
	return strings.Join(parts, "\n")
}

// showPaneTitles reports whether panes get a title line: when there are
// several, and always on the Storage tab, whose panes come and go.
func showPaneTitles(t tab, panes int) bool {
	return panes > 1 || t == tabStorage
}

func layout(cols []col, width int) []table.Column {
	// Each cell gets 2 chars of padding from the default styles.
	avail := width - 2*len(cols)
	total, weights := 0, 0
	for _, c := range cols {
		total += c.min
		weights += c.weight
	}
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = c.min
	}
	if total > avail {
		// Too narrow (e.g. Termux in portrait): take width from the widest
		// flexible columns (weight > 0, like names) first, then from any,
		// so short values like statuses stay readable.
		for deficit := total - max(avail, 0); deficit > 0; deficit-- {
			pick := -1
			for _, flexibleOnly := range []bool{true, false} {
				for i, w := range widths {
					if w > 3 && (!flexibleOnly || cols[i].weight > 0) && (pick < 0 || w > widths[pick]) {
						pick = i
					}
				}
				if pick >= 0 {
					break
				}
			}
			if pick < 0 {
				break
			}
			widths[pick]--
		}
	} else if weights > 0 {
		extra := avail - total
		for i, c := range cols {
			widths[i] += extra * c.weight / weights
		}
	}
	out := make([]table.Column, len(cols))
	for i, c := range cols {
		out[i] = table.Column{Title: c.title, Width: widths[i]}
	}
	return out
}

// tableData returns the panes of the active tab.
func (m Model) tableData() []paneData {
	scope := m.scopeID()
	one := func(title string, f func(string) ([]col, []table.Row, []any)) []paneData {
		cols, rows, items := f(scope)
		return []paneData{{title, cols, rows, items}}
	}
	switch m.tab {
	case tabProjects:
		return one("Projects", m.projectsTable)
	case tabContainers:
		return one("Containers", m.containerTable)
	case tabDatabases:
		return one("Databases", m.databaseTable)
	case tabStorage:
		return m.storagePanes(scope)
	case tabUsers:
		return one("Users", func(string) ([]col, []table.Row, []any) { return m.userTable() })
	case tabBilling:
		return one("Billing", m.billingTable)
	default:
		return m.dashboardPanes(scope)
	}
}

func (m Model) projectsTable(scope string) ([]col, []table.Row, []any) {
	cols := []col{
		{"Project", 12, 3}, {"Containers", 10, 1}, {"Not ready", 9, 1},
		{"Databases", 9, 1}, {"Spend MTD", 12, 1},
	}
	spend := map[string]core.ProjectTotal{}
	for _, t := range m.billing.data.TotalsByProject() {
		spend[t.ProjectID] = t
	}
	var (
		rows  []table.Row
		items []any
	)
	for _, p := range m.projects.data {
		if scope != core.AllProjects && p.ID != scope {
			continue
		}
		var nCont, notReady, nDB int
		for _, c := range m.containers.data {
			if c.ProjectID == p.ID {
				nCont++
				if c.Status != "ready" {
					notReady++
				}
			}
		}
		for _, d := range m.databases.data {
			if d.ProjectID == p.ID {
				nDB++
			}
		}
		t := spend[p.ID]
		rows = append(rows, table.Row{
			p.Name, fmt.Sprint(nCont), fmt.Sprint(notReady), fmt.Sprint(nDB), money(t.Amount, t.Currency),
		})
		items = append(items, p)
	}
	return cols, rows, items
}

func (m Model) containerTable(scope string) ([]col, []table.Row, []any) {
	cols := []col{
		{"Name", 12, 3}, {"Project", 10, 2}, {"Region", 7, 0}, {"Status", 10, 0},
		{"Scale", 6, 0}, {"vCPU/Mem", 12, 0}, {"Endpoint", 10, 4},
	}
	var (
		rows  []table.Row
		items []any
	)
	for _, c := range core.ByProject(m.containers.data, scope, func(c core.Container) string { return c.ProjectID }) {
		rows = append(rows, table.Row{
			c.Name, m.projectName(c.ProjectID), c.Region, m.containerState(c),
			fmt.Sprintf("%d-%d", c.MinScale, c.MaxScale),
			fmt.Sprintf("%.2g/%dM", float64(c.MilliVCPU)/1000, c.MemoryMB),
			c.Endpoint,
		})
		items = append(items, c)
	}
	return cols, rows, items
}

func (m Model) databaseTable(scope string) ([]col, []table.Row, []any) {
	cols := []col{
		{"Name", 12, 3}, {"Project", 10, 2}, {"Region", 7, 0}, {"Engine", 11, 1},
		{"Node", 10, 1}, {"Status", 8, 0}, {"HA", 3, 0}, {"Disk", 6, 0}, {"Endpoint", 10, 3},
	}
	var (
		rows  []table.Row
		items []any
	)
	for _, d := range core.ByProject(m.databases.data, scope, func(d core.Database) string { return d.ProjectID }) {
		rows = append(rows, table.Row{
			d.Name, m.projectName(d.ProjectID), d.Region, d.Engine, d.NodeType, d.Status,
			yesNo(d.HA), fmt.Sprintf("%dG", d.VolumeGB), d.Endpoint,
		})
		items = append(items, d)
	}
	return cols, rows, items
}

func (m Model) userTable() ([]col, []table.Row, []any) {
	cols := []col{
		{"Email", 16, 4}, {"Username", 10, 2}, {"Type", 6, 0}, {"Status", 9, 0},
		{"MFA", 3, 0}, {"Last login", 16, 0},
	}
	var (
		rows  []table.Row
		items []any
	)
	for _, u := range m.users.data {
		last := "never"
		if !u.LastLoginAt.IsZero() {
			last = u.LastLoginAt.Local().Format("2006-01-02 15:04")
		}
		rows = append(rows, table.Row{u.Email, u.Username, u.Type, u.Status, yesNo(u.MFA), last})
		items = append(items, u)
	}
	return cols, rows, items
}

func (m Model) billingTable(scope string) ([]col, []table.Row, []any) {
	cols := []col{
		{"Project", 10, 2}, {"Category", 10, 2}, {"Product", 12, 3},
		{"Resource", 10, 3}, {"Quantity", 10, 1}, {"Amount", 12, 0},
	}
	var (
		rows  []table.Row
		items []any
	)
	for _, l := range core.ByProject(m.billing.data.Lines, scope, func(l core.SpendLine) string { return l.ProjectID }) {
		qty := strings.TrimSpace(l.Quantity + " " + l.Unit)
		rows = append(rows, table.Row{
			l.ProjectName, l.Category, l.Product, l.Resource, qty, money(l.Amount, l.Currency),
		})
		items = append(items, l)
	}
	return cols, rows, items
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
