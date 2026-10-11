package core

import "sort"

// ByProject keeps the items whose project matches projectID.
// AllProjects returns items unchanged.
func ByProject[T any](items []T, projectID string, project func(T) string) []T {
	if projectID == AllProjects {
		return items
	}
	var out []T
	for _, it := range items {
		if project(it) == projectID {
			out = append(out, it)
		}
	}
	return out
}

// ProjectTotal is the summed spend of one project.
type ProjectTotal struct {
	ProjectID   string
	ProjectName string
	Amount      float64
	Currency    string
}

// TotalsByProject sums spend lines per project, largest first.
func (s Spend) TotalsByProject() []ProjectTotal {
	idx := map[string]int{}
	var out []ProjectTotal
	for _, l := range s.Lines {
		i, ok := idx[l.ProjectID]
		if !ok {
			i = len(out)
			idx[l.ProjectID] = i
			out = append(out, ProjectTotal{ProjectID: l.ProjectID, ProjectName: l.ProjectName, Currency: l.Currency})
		}
		out[i].Amount += l.Amount
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Amount > out[j].Amount })
	return out
}

// Total sums all lines. Currency is taken from the first line.
func (s Spend) Total() (float64, string) {
	var sum float64
	cur := ""
	for _, l := range s.Lines {
		sum += l.Amount
		if cur == "" {
			cur = l.Currency
		}
	}
	return sum, cur
}
