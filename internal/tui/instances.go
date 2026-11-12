package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/pvanek/scwpod/internal/core"
)

// The Instances tab lists virtual machines (Scaleway Instances).

func (m Model) scopedInstances(scope string) []core.Instance {
	return core.ByProject(m.instances.data, scope, func(i core.Instance) string { return i.ProjectID })
}

// instancePane is the Instances tab table: inventory columns followed by
// current CPU, memory, disk I/O and network traffic from Cockpit. Narrower screens drop
// the image and private IP, then zone and public IP, then the last metrics.
func (m Model) instancePane(scope string) paneData {
	ins := m.scopedInstances(scope)
	fixed := []col{
		{"Name", 12, 3, left}, {"Project", 10, 2, left}, {"Zone", 8, 0, left}, {"Status", 9, 0, left},
		{"Type", 9, 1, left}, {"Image", 10, 2, left}, {"Public IP", 15, 1, left}, {"Private IP", 10, 1, left},
	}
	// Columns kept per screen width, by index into fixed.
	keep := []int{0, 1, 3, 4} // name, project, status, type
	switch {
	case m.width >= 160:
		keep = []int{0, 1, 2, 3, 4, 5, 6, 7}
	case m.width >= 100:
		keep = []int{0, 1, 2, 3, 4, 6} // without image and private IP
	}
	cols := make([]col, len(keep))
	for i, k := range keep {
		cols[i] = fixed[k]
	}
	return m.metricTableLinks("Instances", cols, instanceMetricCols, len(ins),
		func(i int) ([]string, core.ResourceMetrics, any) {
			in := ins[i]
			all := []string{in.Name, m.projectName(in.ProjectID), in.Zone, instanceState(in), in.Type,
				in.Image, strings.Join(in.PublicIPs, ", "), in.PrivateIP}
			cells := make([]string, len(keep))
			for j, k := range keep {
				cells[j] = all[k]
			}
			return cells, m.instanceMetrics(in), in
		}, false)
}

// instanceMetrics returns an Instance's metrics with CPU in cores and
// memory in bytes turned into a percentage of its type's size when known.
func (m Model) instanceMetrics(in core.Instance) core.ResourceMetrics {
	rm := m.metrics.data.ByResource[in.ID]
	if rm == nil {
		return nil
	}
	out := core.ResourceMetrics{}
	for kind, mt := range rm {
		switch {
		case kind == core.MetricCPU && mt.Unit == core.UnitCores && in.VCPUs > 0:
			mt = scaleMetric(mt, 100/float64(in.VCPUs), core.UnitPercent)
		case kind == core.MetricMemory && mt.Unit == core.UnitBytes && in.RAMBytes > 0:
			mt = scaleMetric(mt, 100/float64(in.RAMBytes), core.UnitPercent)
		}
		out[kind] = mt
	}
	return out
}

// countInstances counts running, stopped and other (transitional or
// locked) Instances.
func countInstances(ins []core.Instance) (running, stopped, other int) {
	for _, in := range ins {
		switch in.Status {
		case "running":
			running++
		case "stopped", "stopped in place":
			stopped++
		default:
			other++
		}
	}
	return running, stopped, other
}

// instanceState is the status, highlighted when the Instance is not
// running or stopped.
func instanceState(in core.Instance) string {
	switch in.Status {
	case "running":
		return in.Status
	case "stopped", "stopped in place":
		return dim.Render(in.Status)
	case "locked":
		return errStyle.Render(in.Status)
	}
	return warn.Render(in.Status)
}

// instancesLine counts the scope's Instances by state.
func (m Model) instancesLine() string {
	running, stopped, other := countInstances(m.scopedInstances(m.scopeID()))
	s := fmt.Sprintf("Instances %d · running %d · stopped %d", running+stopped+other, running, stopped)
	if other > 0 {
		s += " · " + warn.Render(fmt.Sprintf("other %d", other))
	}
	s = ansi.Truncate(s, max(m.width, 20), "…")
	if note := m.tokenNote(); note != "" {
		s += "\n" + note
	}
	return s
}

func (m Model) instanceDetail(s *sheet, in core.Instance) {
	s.section("General")
	s.field("ID", in.ID)
	s.field("Name", in.Name)
	s.field("Project", m.projectName(in.ProjectID)+dim.Render(" "+in.ProjectID))
	s.field("Zone", in.Zone)
	status := instanceState(in)
	if in.StateDetail != "" {
		status += dim.Render(" " + in.StateDetail)
	}
	s.field("Status", status)
	size := ""
	if in.VCPUs > 0 {
		size = fmt.Sprintf(" (%d vCPU, %s RAM)", in.VCPUs, fmtBytes(float64(in.RAMBytes)))
	}
	s.field("Type", in.Type+dim.Render(size))
	s.field("Architecture", in.Arch)
	s.field("Image", in.Image)
	s.field("Protected", yesNo(in.Protected))
	if in.EndOfService {
		s.field("End of service", warn.Render("this type is being retired"))
	}
	s.field("Created", timeStr(in.CreatedAt))
	s.field("Updated", timeStr(in.UpdatedAt))

	rm := m.instanceMetrics(in)
	metricsSection(s, rm, instanceMetricCols)
	if len(rm) == 0 && in.Status == "running" && len(m.metrics.data.ByResource) > 0 {
		s.field("", dim.Render("Instance metrics come from the Scaleway monitoring agent; is it installed?"))
	}

	s.section("Network")
	s.field("Hostname", in.Hostname)
	s.list("Public IPs", in.PublicIPs)
	s.field("Private IP", in.PrivateIP)
	s.field("Private networks", fmt.Sprint(in.PrivateNICs))
	s.field("Security group", in.SecurityGroup)
	s.field("Placement group", in.PlacementGroup)

	s.section("Storage")
	s.list("Volumes", in.Volumes)

	s.section("Maintenance")
	s.list("Scheduled", in.Maintenances)
	s.list("Tags", in.Tags)
}
