package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/pvanek/scwpod/internal/core"
)

const labelWidth = 18

var (
	sectionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86"))
	labelStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Width(labelWidth)
)

// detailView is the full-screen detail of one table row.
type detailView struct {
	item any // core.Project, core.Container, core.Database, core.StorageItem, core.User or core.SpendLine
	vp   viewport.Model
}

func (m *Model) openDetail() {
	p := m.cur()
	if p == nil {
		return
	}
	i := p.table.Cursor()
	if i < 0 || i >= len(p.items) {
		return
	}
	m.detail = &detailView{item: p.items[i], vp: viewport.New()}
	m.openErr = nil
	m.syncDetail()
}

// syncDetail re-resolves the shown item after a data refresh and re-renders
// the viewport content for the current size.
func (m *Model) syncDetail() {
	if m.detail == nil {
		return
	}
	key := itemKey(m.detail.item)
	for _, p := range m.panes {
		for _, it := range p.items {
			if key != "" && itemKey(it) == key {
				m.detail.item = it
			}
		}
	}
	m.detail.vp.SetWidth(max(m.width, 20))
	m.detail.vp.SetHeight(max(m.height-4, 3)) // tabs, scope, title, footer
	m.detail.vp.SetContent(m.detailBody(m.detail.item))
}

// handleDetailKey handles keys while a detail is open. Keys that switch the
// tab or project close the detail and are then handled as usual.
func (m *Model) handleDetailKey(msg tea.KeyPressMsg) tea.Cmd {
	switch k := msg.String(); k {
	case "esc", "backspace", "left", "h":
		m.detail = nil
		return nil
	case "m":
		m.openLink("metrics")
		return nil
	case "l", "L":
		m.openLink("logs")
		return nil
	case "f1", "?":
		m.openHelp()
		return nil
	case "q", "ctrl+c", "r", "a":
		cmd, _ := m.handleKey(k)
		return cmd
	case "tab", "shift+tab", "0", "1", "2", "3", "4", "5", "6", "p", "P":
		m.detail = nil
		cmd, _ := m.handleKey(k)
		m.rebuildTable()
		return cmd
	}
	var cmd tea.Cmd
	m.detail.vp, cmd = m.detail.vp.Update(msg)
	return cmd
}

func (m Model) renderDetail() string {
	title, _ := detailTitle(m.detail.item)
	titleLine := activeTab.Render(title)
	if m.openErr != nil {
		titleLine += " " + errStyle.Render("⚠ "+m.openErr.Error())
	}
	return strings.Join([]string{
		m.tabsLine(),
		m.scopeLine(),
		titleLine,
		m.detail.vp.View(),
		m.statusBar("F1 help · esc back · ↑/↓ scroll · m open metrics · l open logs · r refresh"),
	}, "\n")
}

// itemKey identifies an item across refreshes; "" if it has no identity.
func itemKey(it any) string {
	id := func(prefix, id string) string {
		if id == "" {
			return ""
		}
		return prefix + id
	}
	switch v := it.(type) {
	case core.Project:
		return id("project/", v.ID)
	case core.Container:
		return id("container/", v.ID)
	case core.Database:
		return id("database/", v.ID)
	case core.StorageItem:
		return id("storage/"+v.Kind+"/", v.ID)
	case core.User:
		return id("user/", v.ID)
	case core.SpendLine:
		return strings.Join([]string{"spend", v.ProjectID, v.Category, v.Product, v.Resource, v.SKU, v.ConsumerID}, "/")
	}
	return ""
}

func detailTitle(it any) (string, string) {
	switch v := it.(type) {
	case core.Project:
		return "Project " + v.Name, v.ID
	case core.Container:
		return "Container " + v.Name, v.ID
	case core.Database:
		return "Database " + v.Name, v.ID
	case core.StorageItem:
		return storageTitles[v.Kind] + ": " + v.Name, v.ID
	case core.User:
		return "User " + v.Email, v.ID
	case core.SpendLine:
		return "Consumption " + v.Product, ""
	}
	return "", ""
}

// sheet accumulates labelled lines, wrapping values to the screen width.
type sheet struct {
	width int
	b     strings.Builder
}

func (s *sheet) section(title string) {
	if s.b.Len() > 0 {
		s.b.WriteString("\n")
	}
	s.b.WriteString(sectionStyle.Render(title) + "\n")
}

func (s *sheet) field(label, value string) {
	if value == "" {
		value = dim.Render("—")
	}
	wrapped := lipgloss.NewStyle().Width(max(s.width-labelWidth-1, 10)).Render(value)
	for i, line := range strings.Split(wrapped, "\n") {
		l := ""
		if i == 0 {
			l = label
		}
		s.b.WriteString(labelStyle.Render(l) + " " + line + "\n")
	}
}

// list writes one line per value under label, or "none".
func (s *sheet) list(label string, values []string) {
	if len(values) == 0 {
		s.field(label, dim.Render("none"))
		return
	}
	for i, v := range values {
		if i > 0 {
			label = ""
		}
		s.field(label, v)
	}
}

func (m Model) detailBody(it any) string {
	s := &sheet{width: max(m.width, 30)}
	switch v := it.(type) {
	case core.Project:
		m.projectDetail(s, v)
	case core.Container:
		m.containerDetail(s, v)
	case core.Database:
		m.databaseDetail(s, v)
	case core.StorageItem:
		m.storageDetail(s, v)
	case core.User:
		userDetail(s, v)
	case core.SpendLine:
		spendDetail(s, v)
	}
	return strings.TrimRight(s.b.String(), "\n")
}

func (m Model) projectDetail(s *sheet, p core.Project) {
	s.section("Project")
	s.field("ID", p.ID)
	s.field("Name", p.Name)
	s.field("Description", p.Description)
	s.field("Created", timeStr(p.CreatedAt))

	var conts, dbs []string
	for _, c := range m.containers.data {
		if c.ProjectID == p.ID {
			conts = append(conts, fmt.Sprintf("%s  %s  %s", c.Name, m.containerState(c), c.Region))
		}
	}
	for _, d := range m.databases.data {
		if d.ProjectID == p.ID {
			dbs = append(dbs, fmt.Sprintf("%s  %s  %s  %s", d.Name, d.Engine, d.Status, d.Region))
		}
	}
	var storage []string
	for _, st := range m.storageItems(p.ID) {
		line := st.Kind + "  " + st.Name + "  " + st.Location
		if st.SizeBytes > 0 {
			line += "  " + fmtBytes(float64(st.SizeBytes))
		}
		storage = append(storage, line)
	}
	s.section("Resources")
	s.list("Containers", conts)
	s.list("Databases", dbs)
	s.list("Storage", storage)

	lines := core.ByProject(m.billing.data.Lines, p.ID, func(l core.SpendLine) string { return l.ProjectID })
	total, cur := core.Spend{Lines: lines}.Total()
	s.section("Spend " + m.billing.data.Period)
	s.field("Consumption", money(total, cur))
	cats := map[string]float64{}
	for _, l := range lines {
		cats[l.Category] += l.Amount
	}
	names := make([]string, 0, len(cats))
	for c := range cats {
		names = append(names, c)
	}
	sort.Slice(names, func(i, j int) bool { return cats[names[i]] > cats[names[j]] })
	for _, c := range names {
		s.field(c, money(cats[c], cur))
	}
}

func (m Model) containerDetail(s *sheet, c core.Container) {
	s.section("General")
	s.field("ID", c.ID)
	s.field("Name", c.Name)
	s.field("Description", c.Description)
	s.field("Project", m.projectName(c.ProjectID)+dim.Render(" "+c.ProjectID))
	s.field("Namespace", c.Namespace)
	s.field("Region", c.Region)
	status := c.Status
	if c.ErrorReason != "" {
		status = errStyle.Render(status)
	}
	s.field("Status", status)
	if c.ErrorReason != "" {
		s.field("Error", errStyle.Render(c.ErrorReason))
	}
	running := dim.Render("unknown (needs Cockpit metrics)")
	if n, ok := m.runningInstances(c); ok {
		running = fmt.Sprintf("%d instance(s)", n)
		if n == 0 {
			running = "idle, scaled to zero"
		}
	}
	s.field("Running", running+dim.Render(fmt.Sprintf(" (scale %d–%d)", c.MinScale, c.MaxScale)))
	s.field("Created", timeStr(c.CreatedAt))
	s.field("Updated", timeStr(c.UpdatedAt))

	metricsSection(s, m.containerMetrics(c), containerMetricCols)
	m.linksSection(s, c)

	s.section("Runtime")
	s.field("Image", c.Image)
	s.field("Resources", fmt.Sprintf("%g vCPU, %d MB", float64(c.MilliVCPU)/1000, c.MemoryMB))
	s.field("Scale", fmt.Sprintf("%d – %d instances", c.MinScale, c.MaxScale))
	s.field("Timeout", durationStr(c.Timeout))
	s.field("Protocol / port", fmt.Sprintf("%s / %d", c.Protocol, c.Port))
	s.field("Privacy", c.Privacy)
	s.field("Endpoint", c.Endpoint)
	s.field("Private endpoint", c.PrivateEndpoint)

	s.section("Environment")
	keys := make([]string, 0, len(c.Env))
	for k := range c.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, k+"="+c.Env[k])
	}
	s.list("Variables", env)
	s.list("Tags", c.Tags)
}

func (m Model) databaseDetail(s *sheet, d core.Database) {
	s.section("General")
	s.field("ID", d.ID)
	s.field("Name", d.Name)
	s.field("Project", m.projectName(d.ProjectID)+dim.Render(" "+d.ProjectID))
	s.field("Region", d.Region)
	s.field("Status", d.Status)
	s.field("Engine", d.Engine)
	s.field("Node type", d.NodeType)
	s.field("High availability", yesNo(d.HA))
	s.field("Read replicas", fmt.Sprint(d.ReadReplicas))
	s.field("Created", timeStr(d.CreatedAt))

	metricsSection(s, m.metrics.data.ByResource[d.ID], databaseMetricCols)
	m.linksSection(s, d)

	s.section("Storage")
	s.field("Volume", fmt.Sprintf("%d GB %s", d.VolumeGB, d.VolumeType))
	s.field("Encrypted", yesNo(d.Encrypted))
	s.list("Endpoints", d.Endpoints)

	s.section("Backups")
	if d.BackupEnabled {
		s.field("Schedule", fmt.Sprintf("every %dh, kept %d days", d.BackupEveryH, d.BackupKeepDays))
		s.field("Next backup", timeStr(d.NextBackupAt))
	} else {
		s.field("Schedule", warn.Render("disabled"))
	}

	s.section("Maintenance")
	s.list("Pending", d.Maintenances)
	s.list("Upgrades to", d.Upgrades)
	s.list("Tags", d.Tags)
}

func userDetail(s *sheet, u core.User) {
	s.section("User")
	s.field("ID", u.ID)
	s.field("Email", u.Email)
	s.field("Username", u.Username)
	s.field("Name", strings.TrimSpace(u.FirstName+" "+u.LastName))
	s.field("Type", u.Type)
	s.field("Status", u.Status)
	mfa := yesNo(u.MFA)
	if !u.MFA {
		mfa = warn.Render("no")
	}
	s.field("MFA", mfa)
	s.field("Locked", yesNo(u.Locked))
	s.field("Created", timeStr(u.CreatedAt))
	s.field("Last login", timeStr(u.LastLoginAt))
	s.list("Tags", u.Tags)
}

func spendDetail(s *sheet, l core.SpendLine) {
	s.section("Consumption")
	s.field("Project", l.ProjectName+dim.Render(" "+l.ProjectID))
	s.field("Category", l.Category)
	s.field("Product", l.Product)
	s.field("Resource", l.Resource)
	s.field("SKU", l.SKU)
	s.field("Quantity", strings.TrimSpace(l.Quantity+" "+l.Unit))
	s.field("Amount", money(l.Amount, l.Currency))
	s.field("Consumer ID", l.ConsumerID)
}

func timeStr(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04")
}

func durationStr(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return d.String()
}
