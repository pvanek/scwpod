package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/pvanek/scwpod/internal/core"
)

var (
	box      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
	boxTitle = lipgloss.NewStyle().Bold(true)
	bold     = lipgloss.NewStyle().Bold(true)
	barStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("63"))
	warn     = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)

const barWidth = 16

// billingPanel renders the current month, invoice history and the category
// breakdown side by side, stacking them when the terminal is too narrow.
func (m Model) billingPanel() string {
	current := box.Render(m.currentMonthBox())
	history := box.Render(m.historyBox())

	row := lipgloss.JoinHorizontal(lipgloss.Top, current, history)
	if lipgloss.Width(row) > m.width {
		return lipgloss.JoinVertical(lipgloss.Left, current, history)
	}
	if cat := box.Render(m.categoryBox()); lipgloss.Width(row)+lipgloss.Width(cat) <= m.width {
		row = lipgloss.JoinHorizontal(lipgloss.Top, row, cat)
	}
	return row
}

func (m Model) currentMonthBox() string {
	b := m.billing.data
	spend := m.scopedSpend()
	consumption, cur := spend.Total()
	if cur == "" {
		cur = m.currency()
	}
	asOf := b.UpdatedAt
	if asOf.IsZero() {
		asOf = m.now()
	}

	var lines []string
	add := func(label, value string) { lines = append(lines, fmt.Sprintf("%-15s %14s", label, value)) }

	title := "This month " + b.Period
	lines = append(lines, boxTitle.Render(title))
	if m.scopeID() == core.AllProjects {
		total := consumption - b.Discount + b.Tax
		add("Consumption", money(consumption, cur))
		add("Discounts", money(-b.Discount, cur))
		add("Tax", money(b.Tax, cur))
		lines = append(lines, bold.Render(fmt.Sprintf("%-15s %14s", "Total", money(total, cur))))
		lines = append(lines, m.forecastLine(total, b.Period, asOf, cur, true))
	} else {
		add("Consumption", money(consumption, cur))
		lines = append(lines, m.forecastLine(consumption, b.Period, asOf, cur, false))
		lines = append(lines, dim.Render("excl. tax and discounts,"), dim.Render("which are org-wide"))
	}
	if !b.UpdatedAt.IsZero() {
		lines = append(lines, dim.Render("as of "+b.UpdatedAt.Local().Format("2006-01-02 15:04")))
	}
	return strings.Join(lines, "\n")
}

// forecastLine extrapolates amount to month end and, for the whole
// organization, compares it with last month's invoice.
func (m Model) forecastLine(amount float64, period string, asOf time.Time, cur string, compare bool) string {
	f := core.Forecast(amount, period, asOf)
	line := fmt.Sprintf("%-15s %14s", "Forecast", money(f, cur))
	if !compare || f == 0 {
		return line
	}
	prev := previousPeriod(period)
	for _, mt := range m.billing.data.Months() {
		if mt.Period == prev && mt.Taxed > 0 {
			delta := (f - mt.Taxed) / mt.Taxed * 100
			style := dim
			if delta > 10 {
				style = warn
			}
			line += style.Render(fmt.Sprintf(" %+.0f%%", delta))
		}
	}
	return line
}

func (m Model) historyBox() string {
	months := m.billing.data.Months()
	lines := []string{boxTitle.Render("Invoices, incl. tax") + dim.Render(" (org)")}
	if len(months) == 0 {
		lines = append(lines, dim.Render(fmt.Sprintf("none in the last %d months", core.HistoryMonths)))
		return strings.Join(lines, "\n")
	}

	shown := months
	if limit := max(3, m.height/4); len(shown) > limit {
		shown = shown[:limit]
	}
	var peak float64
	for _, mt := range months {
		peak = max(peak, mt.Taxed)
	}
	for _, mt := range shown {
		state := dim.Render(mt.State)
		if mt.State != "paid" {
			state = warn.Render(mt.State)
		}
		lines = append(lines, fmt.Sprintf("%s %s %14s %s",
			mt.Period, bar(mt.Taxed, peak), money(mt.Taxed, mt.Currency), state))
	}
	if hidden := len(months) - len(shown); hidden > 0 {
		lines = append(lines, dim.Render(fmt.Sprintf("… %d older", hidden)))
	}

	total, avg, ytd := m.billing.data.HistoryTotals(m.now())
	cur := months[0].Currency
	lines = append(lines,
		fmt.Sprintf("%-24s %14s", fmt.Sprintf("Last %d months", len(months)), money(total, cur)),
		fmt.Sprintf("%-24s %14s", "Monthly average", money(avg, cur)),
		fmt.Sprintf("%-24s %14s", m.now().Format("2006")+" so far (invoiced)", money(ytd, cur)),
	)
	return strings.Join(lines, "\n")
}

func (m Model) categoryBox() string {
	totals := map[string]float64{}
	spend := m.scopedSpend()
	for _, l := range spend.Lines {
		totals[l.Category] += l.Amount
	}
	sum, cur := spend.Total()
	if cur == "" {
		cur = m.currency()
	}
	cats := make([]string, 0, len(totals))
	for c := range totals {
		cats = append(cats, c)
	}
	sort.Slice(cats, func(i, j int) bool { return totals[cats[i]] > totals[cats[j]] })

	lines := []string{boxTitle.Render("By category")}
	if len(cats) == 0 {
		lines = append(lines, dim.Render("no consumption yet"))
	}
	for i, c := range cats {
		if i == 6 {
			lines = append(lines, dim.Render(fmt.Sprintf("… %d more", len(cats)-i)))
			break
		}
		pct := 0.0
		if sum > 0 {
			pct = totals[c] / sum * 100
		}
		lines = append(lines, fmt.Sprintf("%-16s %12s %s", truncate(c, 16), money(totals[c], cur), dim.Render(fmt.Sprintf("%3.0f%%", pct))))
	}
	return strings.Join(lines, "\n")
}

// currency guesses the organization currency when there is no consumption.
func (m Model) currency() string {
	if months := m.billing.data.Months(); len(months) > 0 {
		return months[0].Currency
	}
	return "EUR"
}

func bar(v, peak float64) string {
	n := 0
	if peak > 0 {
		n = int(v / peak * barWidth)
	}
	if v > 0 && n == 0 {
		n = 1
	}
	return barStyle.Render(strings.Repeat("█", n)) + strings.Repeat(" ", barWidth-n)
}

func previousPeriod(period string) string {
	t, err := time.Parse("2006-01", period)
	if err != nil {
		return ""
	}
	return core.CurrentPeriod(t.AddDate(0, -1, 0))
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
