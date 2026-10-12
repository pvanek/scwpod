package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/pvanek/scwpod/internal/core"
)

// The Storage tab has one table per storage kind that has any items.

// fetchBuckets lists buckets of every project; it needs the project list.
func (m *Model) fetchBuckets() tea.Cmd {
	if len(m.projects.data) == 0 {
		return nil
	}
	ids := make([]string, len(m.projects.data))
	for i, p := range m.projects.data {
		ids[i] = p.ID
	}
	m.buckets.loading = true
	return fetch(func(ctx context.Context) (bucketList, error) {
		items, err := m.svc.Buckets(ctx, ids)
		return bucketList(items), err
	})
}

// bucketList distinguishes the bucket fetch from the other storage.
type bucketList []core.StorageItem

// storageItems is all storage of the scope, buckets first.
func (m Model) storageItems(scope string) []core.StorageItem {
	all := append(append([]core.StorageItem(nil), m.buckets.data...), m.storage.data...)
	return core.ByProject(all, scope, func(s core.StorageItem) string { return s.ProjectID })
}

// storageLine summarizes the scope's storage per kind.
func (m Model) storageLine() string {
	count, size := map[string]int{}, map[string]uint64{}
	for _, s := range m.storageItems(m.scopeID()) {
		count[s.Kind]++
		size[s.Kind] += s.SizeBytes
	}
	parts := make([]string, 0, len(core.StorageKinds))
	for _, k := range core.StorageKinds {
		p := fmt.Sprintf("%s %d", k, count[k])
		if size[k] > 0 {
			p += dim.Render(" (" + fmtBytes(float64(size[k])) + ")")
		}
		parts = append(parts, p)
	}
	return ansi.Truncate(strings.Join(parts, dim.Render(" · ")), max(m.width, 20), "…")
}

func (m Model) storagePanes(scope string) []paneData {
	byKind := map[string][]core.StorageItem{}
	for _, s := range m.storageItems(scope) {
		byKind[s.Kind] = append(byKind[s.Kind], s)
	}
	var out []paneData
	for _, k := range core.StorageKinds {
		if items := byKind[k]; len(items) > 0 {
			out = append(out, m.storagePane(k, items))
		}
	}
	if len(out) == 0 {
		out = append(out, paneData{title: "Storage", cols: []col{
			{"Name", 12, 3}, {"Project", 10, 2}, {"Location", 8, 0}, {"Status", 9, 0}, {"Size", 7, 0},
		}})
	}
	return out
}

var storageTitles = map[string]string{
	core.StorageObject:   "Object Storage buckets",
	core.StorageRegistry: "Container Registry namespaces",
	core.StorageBlock:    "Block Storage volumes",
	core.StorageLocal:    "Local volumes",
	core.StorageFile:     "File Storage",
}

func (m Model) storagePane(kind string, items []core.StorageItem) paneData {
	name, project := col{"Name", 12, 3}, col{"Project", 10, 2}
	status, size := col{"Status", 9, 0}, col{"Size", 7, 0}
	var cols []col
	var row func(s core.StorageItem) []string
	switch kind {
	case core.StorageObject:
		cols = []col{{"Name", 12, 2}, project, {"Region", 7, 0}, {"Created", 16, 0}, {"Endpoint", 10, 4}}
		row = func(s core.StorageItem) []string {
			return []string{s.Name, m.projectName(s.ProjectID), s.Location, timeStr(s.CreatedAt), s.Endpoint}
		}
	case core.StorageRegistry:
		cols = []col{name, project, {"Region", 7, 0}, status, {"Access", 7, 0}, {"Images", 6, 0}, size, {"Endpoint", 10, 3}}
		row = func(s core.StorageItem) []string {
			return []string{s.Name, m.projectName(s.ProjectID), s.Location, s.Status, visibility(s.Public),
				countStr(s.Images), sizeStr(s.SizeBytes), s.Endpoint}
		}
	case core.StorageFile:
		cols = []col{name, project, {"Region", 7, 0}, status, size, {"Mounts", 6, 0}}
		row = func(s core.StorageItem) []string {
			return []string{s.Name, m.projectName(s.ProjectID), s.Location, s.Status, sizeStr(s.SizeBytes), countStr(s.Attachments)}
		}
	default: // block and local volumes
		cols = []col{name, project, {"Zone", 8, 0}, status, {"Type", 8, 0}, size, {"Attached to", 11, 2}}
		row = func(s core.StorageItem) []string {
			return []string{s.Name, m.projectName(s.ProjectID), s.Location, s.Status, s.Class,
				sizeStr(s.SizeBytes), strings.Join(s.AttachedTo, ", ")}
		}
	}
	d := paneData{title: storageTitles[kind], cols: cols}
	for _, s := range items {
		d.rows = append(d.rows, table.Row(row(s)))
		d.items = append(d.items, s)
	}
	return d
}

func (m Model) storageDetail(s *sheet, it core.StorageItem) {
	s.section(storageTitles[it.Kind])
	if it.ID != it.Name {
		s.field("ID", it.ID)
	}
	s.field("Name", it.Name)
	s.field("Description", it.Description)
	s.field("Project", m.projectName(it.ProjectID)+dim.Render(" "+it.ProjectID))
	s.field("Location", it.Location)
	if it.Status != "" {
		status := it.Status
		if it.StatusMessage != "" {
			status += dim.Render(" " + it.StatusMessage)
		}
		s.field("Status", status)
	}
	s.field("Created", timeStr(it.CreatedAt))
	if !it.UpdatedAt.IsZero() {
		s.field("Updated", timeStr(it.UpdatedAt))
	}

	s.section("Details")
	switch it.Kind {
	case core.StorageObject:
		s.field("Size", dim.Render("not available over the S3 API"))
	default:
		s.field("Size", sizeStr(it.SizeBytes))
	}
	if it.Class != "" {
		s.field("Type", it.Class)
	}
	if it.IOPS > 0 {
		s.field("Performance", fmt.Sprintf("%d IOPS", it.IOPS))
	}
	switch it.Kind {
	case core.StorageBlock, core.StorageLocal:
		s.list("Attached to", it.AttachedTo)
	case core.StorageFile:
		s.field("Mounts", countStr(it.Attachments))
	case core.StorageRegistry:
		s.field("Access", visibility(it.Public))
		s.field("Images", countStr(it.Images))
	}
	if it.Endpoint != "" {
		s.field("Endpoint", it.Endpoint)
	}
	if it.Kind != core.StorageObject {
		s.list("Tags", it.Tags)
	}
}

func visibility(public bool) string {
	if public {
		return "public"
	}
	return "private"
}

func countStr(n *int) string {
	if n == nil {
		return "–"
	}
	return fmt.Sprint(*n)
}

func sizeStr(b uint64) string {
	if b == 0 {
		return "–"
	}
	return fmtBytes(float64(b))
}
