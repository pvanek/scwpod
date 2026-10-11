package core

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	billing "github.com/scaleway/scaleway-sdk-go/api/billing/v2beta1"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

// HistoryMonths is how far back Billing fetches invoices.
const HistoryMonths = 12

// Invoice is one issued invoice.
type Invoice struct {
	ID       string
	Number   int32
	Period   time.Time // first day of the billed month
	Issued   time.Time
	Due      time.Time
	Untaxed  float64
	Taxed    float64
	Currency string
	State    string
	Type     string
}

// Billing is the current period's consumption plus invoice history.
// Discount, Tax and Invoices are organization-wide.
type Billing struct {
	Spend
	Discount float64   // discount applied to the current period, positive
	Tax      float64   // tax due on the current period so far
	Invoices []Invoice // newest first
}

// MonthTotal sums the invoices issued for one billing month.
type MonthTotal struct {
	Period   string // "YYYY-MM"
	Untaxed  float64
	Taxed    float64
	Currency string
	State    string // "paid" only if every invoice of the month is paid
}

// Billing fetches consumption and taxes for period and the invoices of the
// last HistoryMonths months. Consumption is required; failures of the other
// calls are returned as an error alongside the partial result.
func (s *Service) Billing(ctx context.Context, period string, now time.Time) (Billing, error) {
	var (
		wg                       sync.WaitGroup
		b                        Billing
		spendErr, taxErr, invErr error
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		b.Spend, b.Discount, spendErr = s.consumption(ctx, period)
	}()
	go func() {
		defer wg.Done()
		b.Tax, taxErr = s.taxes(ctx, period)
	}()
	go func() {
		defer wg.Done()
		b.Invoices, invErr = s.invoices(ctx, now.AddDate(0, -HistoryMonths, 0))
	}()
	wg.Wait()
	if spendErr != nil {
		return Billing{}, spendErr
	}
	return b, errors.Join(taxErr, invErr)
}

func (s *Service) consumption(ctx context.Context, period string) (Spend, float64, error) {
	resp, err := s.billing.ListConsumptions(&billing.ListConsumptionsRequest{
		OrganizationID: &s.orgID,
		BillingPeriod:  &period,
	}, scw.WithContext(ctx), scw.WithAllPages())
	if err != nil {
		return Spend{}, 0, fmt.Errorf("listing consumption for %s: %w", period, err)
	}
	spend := Spend{Period: period, UpdatedAt: derefTime(resp.UpdatedAt)}
	for _, c := range resp.Consumptions {
		line := SpendLine{
			ProjectID:   c.ProjectID,
			ProjectName: c.ProjectName,
			Category:    c.CategoryName,
			Product:     c.ProductName,
			Resource:    c.ResourceName,
			Quantity:    c.BilledQuantity,
			Unit:        c.Unit,
			SKU:         c.Sku,
			ConsumerID:  c.ConsumerID,
		}
		if c.Value != nil {
			line.Amount = c.Value.ToFloat()
			line.Currency = c.Value.CurrencyCode
		}
		spend.Lines = append(spend.Lines, line)
	}
	sort.Slice(spend.Lines, func(i, j int) bool { return spend.Lines[i].Amount > spend.Lines[j].Amount })
	return spend, math.Abs(resp.TotalDiscountUntaxedValue), nil
}

func (s *Service) taxes(ctx context.Context, period string) (float64, error) {
	resp, err := s.billing.ListTaxes(&billing.ListTaxesRequest{
		OrganizationID: s.orgID,
		BillingPeriod:  &period,
	}, scw.WithContext(ctx), scw.WithAllPages())
	if err != nil {
		return 0, fmt.Errorf("listing taxes for %s: %w", period, err)
	}
	var sum float64
	for _, t := range resp.Taxes {
		if t.TotalTaxValue != nil {
			sum += *t.TotalTaxValue
		}
	}
	return sum, nil
}

func (s *Service) invoices(ctx context.Context, since time.Time) ([]Invoice, error) {
	resp, err := s.billing.ListInvoices(&billing.ListInvoicesRequest{
		OrganizationID:          &s.orgID,
		BillingPeriodStartAfter: &since,
		OrderBy:                 billing.ListInvoicesRequestOrderByStartDateDesc,
	}, scw.WithContext(ctx), scw.WithAllPages())
	if err != nil {
		return nil, fmt.Errorf("listing invoices: %w", err)
	}
	out := make([]Invoice, 0, len(resp.Invoices))
	for _, i := range resp.Invoices {
		inv := Invoice{
			ID:     i.ID,
			Number: i.Number,
			Period: derefTime(i.BillingPeriod),
			Issued: derefTime(i.IssuedDate),
			Due:    derefTime(i.DueDate),
			State:  i.State,
			Type:   i.Type.String(),
		}
		if inv.Period.IsZero() {
			inv.Period = derefTime(i.StartDate)
		}
		if i.TotalUntaxed != nil {
			inv.Untaxed = i.TotalUntaxed.ToFloat()
			inv.Currency = i.TotalUntaxed.CurrencyCode
		}
		if i.TotalTaxed != nil {
			inv.Taxed = i.TotalTaxed.ToFloat()
			inv.Currency = i.TotalTaxed.CurrencyCode
		}
		out = append(out, inv)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Period.After(out[b].Period) })
	return out, nil
}

// Months groups invoices by billing month, newest first.
func (b Billing) Months() []MonthTotal {
	idx := map[string]int{}
	var out []MonthTotal
	for _, inv := range b.Invoices {
		p := CurrentPeriod(inv.Period)
		i, ok := idx[p]
		if !ok {
			i = len(out)
			idx[p] = i
			out = append(out, MonthTotal{Period: p, Currency: inv.Currency, State: "paid"})
		}
		out[i].Untaxed += inv.Untaxed
		out[i].Taxed += inv.Taxed
		if inv.State != "paid" {
			out[i].State = inv.State
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Period > out[j].Period })
	return out
}

// HistoryTotals summarizes invoiced months. yearToDate covers the months of
// now's year; average is per invoiced month.
func (b Billing) HistoryTotals(now time.Time) (total, average, yearToDate float64) {
	months := b.Months()
	year := now.Format("2006")
	for _, m := range months {
		total += m.Taxed
		if m.Period[:4] == year {
			yearToDate += m.Taxed
		}
	}
	if len(months) > 0 {
		average = total / float64(len(months))
	}
	return total, average, yearToDate
}

// Forecast extrapolates amount, spent from the start of period until asOf,
// linearly to the end of the period. It returns 0 if asOf is not inside the
// period. At least one day is assumed elapsed to avoid wild early estimates.
func Forecast(amount float64, period string, asOf time.Time) float64 {
	start, err := time.ParseInLocation("2006-01", period, asOf.Location())
	if err != nil {
		return 0
	}
	end := start.AddDate(0, 1, 0)
	if asOf.Before(start) || !asOf.Before(end) {
		return 0
	}
	elapsed := max(asOf.Sub(start), 24*time.Hour)
	return amount * float64(end.Sub(start)) / float64(elapsed)
}
