package tui

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/pvanek/scwpod/internal/core"
)

// Containers and databases link to their Cockpit Grafana dashboards, in the
// Overview tables (name and Metrics column: metrics, Logs column) and in detail views. The
// links are OSC 8 hyperlinks, clickable in most terminals; "m" and "l" open
// them in the browser for terminals without hyperlink support (or Konsole
// without "Allow escape sequences for links").

var linkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Underline(true)

// linkCell makes table cell text a clickable link; text without a URL is
// returned as is. The table keeps the link intact when it shortens a cell.
// It is only underlined, with no colour, so it stays readable in a highlighted
// row. It turns just the underline off again rather than resetting all
// attributes as lipgloss does, which would cut off the row's highlight.
func linkCell(text, url string) string {
	if url == "" {
		return text
	}
	return ansi.SetHyperlink(url) + "\x1b[4m" + text + "\x1b[24m" + ansi.ResetHyperlink()
}

// linkOrDash is a link cell, or a dash when there is no URL.
func linkOrDash(text, url string) string {
	if url == "" {
		return "–"
	}
	return linkCell(text, url)
}

func (m *Model) fetchGrafanaURLs() tea.Cmd {
	ids := make([]string, len(m.projects.data))
	for i, p := range m.projects.data {
		ids[i] = p.ID
	}
	return fetch(func(ctx context.Context) (map[string]string, error) {
		return m.svc.GrafanaURLs(ctx, ids)
	})
}

// links returns the Grafana links of a container or database.
func (m Model) links(item any) core.Links {
	switch v := item.(type) {
	case core.Container:
		return core.ContainerLinks(m.grafana.data[v.ProjectID], v.Region, m.metrics.data.Names[v.ID])
	case core.Database:
		return core.DatabaseLinks(m.grafana.data[v.ProjectID], v.Region, v.Engine, v.Name)
	}
	return core.Links{}
}

// linksSection adds clickable links to a detail sheet.
func (m Model) linksSection(s *sheet, item any) {
	l := m.links(item)
	if l.Metrics == "" && l.Logs == "" {
		return
	}
	s.section("Cockpit (Grafana)")
	// The full URL is shown so terminals without OSC 8 hyperlinks can still
	// detect or copy it. It is broken into lines that fit, each also a link
	// to the whole URL, since a wrapped plain URL is no longer clickable.
	width := max(s.width-labelWidth-1, 10)
	link := func(label, key, url, note string) {
		if url == "" {
			return
		}
		lines := strings.Split(ansi.Hardwrap(url, width, false), "\n")
		for i, line := range lines {
			lines[i] = linkStyle.Hyperlink(url).Render(line)
		}
		s.field(label, strings.Join(lines, "\n"))
		s.field("", dim.Render("press "+key+" to open"+note))
	}
	note := ""
	if c, ok := item.(core.Container); ok && m.metrics.data.Names[c.ID] == "" {
		note = " · not filtered: container name in Cockpit unknown"
	}
	link("Metrics dashboard", "m", l.Metrics, note)
	link("Logs", "l", l.Logs, note)
}

// opener starts the system browser; replaced in tests.
var opener = func(url string) error {
	if runtime.GOOS == "windows" {
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
	for _, cmd := range []string{"termux-open-url", "xdg-open", "open"} {
		if path, err := exec.LookPath(cmd); err == nil {
			return exec.Command(path, url).Start()
		}
	}
	return fmt.Errorf("no browser opener found (termux-open-url, xdg-open, open)")
}

// openLink opens a link of the open detail's item, or else of the selected
// table row, in the browser.
func (m *Model) openLink(which string) {
	var item any
	if m.detail != nil {
		item = m.detail.item
	} else if p := m.cur(); p != nil {
		if i := p.table.Cursor(); i >= 0 && i < len(p.items) {
			item = p.items[i]
		}
	}
	l := m.links(item)
	url := l.Metrics
	if which == "logs" {
		url = l.Logs
	}
	if url == "" {
		m.openErr = fmt.Errorf("no Grafana link for this row")
		return
	}
	m.openErr = opener(url)
}
