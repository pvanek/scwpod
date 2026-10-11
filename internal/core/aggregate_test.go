package core

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/scaleway/scaleway-sdk-go/scw"
)

func TestTotalsByProject(t *testing.T) {
	s := Spend{Lines: []SpendLine{
		{ProjectID: "a", ProjectName: "alpha", Amount: 1.5, Currency: "EUR"},
		{ProjectID: "b", ProjectName: "beta", Amount: 10, Currency: "EUR"},
		{ProjectID: "a", ProjectName: "alpha", Amount: 2.5, Currency: "EUR"},
	}}
	got := s.TotalsByProject()
	if len(got) != 2 {
		t.Fatalf("want 2 projects, got %d", len(got))
	}
	if got[0].ProjectID != "b" || got[0].Amount != 10 {
		t.Errorf("first = %+v, want beta 10", got[0])
	}
	if got[1].ProjectID != "a" || got[1].Amount != 4 {
		t.Errorf("second = %+v, want alpha 4", got[1])
	}
	if total, cur := s.Total(); total != 14 || cur != "EUR" {
		t.Errorf("Total = %v %s, want 14 EUR", total, cur)
	}
}

func TestByProject(t *testing.T) {
	items := []Database{{Name: "x", ProjectID: "a"}, {Name: "y", ProjectID: "b"}}
	pid := func(d Database) string { return d.ProjectID }
	if got := ByProject(items, AllProjects, pid); len(got) != 2 {
		t.Errorf("AllProjects: got %d items, want 2", len(got))
	}
	if got := ByProject(items, "b", pid); len(got) != 1 || got[0].Name != "y" {
		t.Errorf("project b: got %+v", got)
	}
}

func TestPerRegionKeepsPartialResults(t *testing.T) {
	regions := []scw.Region{scw.RegionFrPar, scw.RegionNlAms}
	got, err := perRegion(regions, func(r scw.Region) ([]string, error) {
		if r == scw.RegionNlAms {
			return nil, errors.New("boom")
		}
		return []string{string(r)}, nil
	})
	if err == nil {
		t.Error("expected error from failing region")
	}
	if len(got) != 1 || got[0] != "fr-par" {
		t.Errorf("got %v, want [fr-par]", got)
	}
}

func TestMonthsAndHistoryTotals(t *testing.T) {
	sep := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	dec := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	b := Billing{Invoices: []Invoice{
		{Period: sep, Untaxed: 100, Taxed: 120, Currency: "EUR", State: "paid"},
		{Period: sep, Untaxed: 10, Taxed: 12, Currency: "EUR", State: "outdated"},
		{Period: dec, Untaxed: 50, Taxed: 60, Currency: "EUR", State: "paid"},
	}}
	months := b.Months()
	if len(months) != 2 || months[0].Period != "2026-09" || months[0].Taxed != 132 || months[0].State != "outdated" {
		t.Fatalf("months = %+v", months)
	}
	total, avg, ytd := b.HistoryTotals(time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC))
	if total != 192 || avg != 96 || ytd != 132 {
		t.Errorf("totals = %v %v %v, want 192 96 132", total, avg, ytd)
	}
}

func TestForecast(t *testing.T) {
	// 10 of 31 days elapsed: 100 -> 310.
	got := Forecast(100, "2026-10", time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC))
	if math.Abs(got-310) > 0.01 {
		t.Errorf("Forecast = %v, want 310", got)
	}
	if got := Forecast(100, "2026-09", time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)); got != 0 {
		t.Errorf("past period forecast = %v, want 0", got)
	}
}
