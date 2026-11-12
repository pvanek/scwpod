package core

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// This file turns metric names available in Cockpit into PromQL queries,
// one set per resource type. Every query aggregates by resourceIDLabel so
// each result series belongs to one container or database.

// ContainerMetricPrefix is the name prefix of Scaleway container metrics.
const ContainerMetricPrefix = "serverless_container_"

// databaseMetricRoot is the name prefix of Scaleway Managed Database metrics.
const databaseMetricRoot = "rdb_instance_"

// statusLabels are label names that may carry the HTTP status code of a
// request metric, in order of preference.
var statusLabels = []string{"status_code", "code", "status", "http_status", "response_code"}

// Query is one PromQL query producing one metric kind per resource.
type Query struct {
	Kind   MetricKind
	Source string // main metric name, for errors and debugging
	PromQL string
	Unit   Unit
}

// SourceInfo is what was discovered about one Cockpit data source.
type SourceInfo struct {
	names       map[string]bool
	StatusLabel string // label with the HTTP status on container requests
	// InstanceCPUMode is set when the Instance CPU counter has a "mode"
	// label (node exporter style), so idle time must be excluded.
	InstanceCPUMode bool
	fetched         time.Time
}

// Names returns the discovered metric names, sorted.
func (i *SourceInfo) Names() []string {
	if i == nil {
		return nil
	}
	out := make([]string, 0, len(i.names))
	for n := range i.names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// has returns the first of the candidate names (each also tried with a
// "_total" suffix) that exists in the data source, or "".
func (i *SourceInfo) has(candidates ...string) string {
	if i == nil {
		return ""
	}
	for _, c := range candidates {
		for _, n := range []string{c, c + "_total"} {
			if i.names[n] {
				return n
			}
		}
	}
	return ""
}

func statusLabel(labels map[string]string) string {
	for _, l := range statusLabels {
		if _, ok := labels[l]; ok {
			return l
		}
	}
	return ""
}

// withMatcher adds a label matcher to a selector like {a="b"}.
func withMatcher(sel, matcher string) string {
	return strings.TrimSuffix(sel, "}") + "," + matcher + "}"
}

func rate(name, sel string) string { return fmt.Sprintf("rate(%s%s[5m])", name, sel) }

func byID(agg, expr string) string {
	return fmt.Sprintf("%s by (%s) (%s)", agg, resourceIDLabel, expr)
}

// --- containers ---

// PickContainerMetrics chooses which discovered metric names to use for
// container CPU, memory, instances, requests and latency. Limits, network
// and payload-size metrics are skipped; shorter names win.
func PickContainerMetrics(names []string) map[MetricKind]string {
	sorted := append([]string(nil), names...)
	sort.Slice(sorted, func(i, j int) bool {
		if len(sorted[i]) != len(sorted[j]) {
			return len(sorted[i]) < len(sorted[j])
		}
		return sorted[i] < sorted[j]
	})
	out := map[MetricKind]string{}
	for _, n := range sorted {
		l := strings.ToLower(n)
		if !strings.HasPrefix(l, ContainerMetricPrefix) || strings.Contains(l, "limit") {
			continue
		}
		var kind MetricKind
		switch {
		case (strings.Contains(l, "duration") || strings.Contains(l, "latency")) && strings.HasSuffix(l, "_bucket"):
			kind = MetricLatency
		case strings.Contains(l, "duration") || strings.Contains(l, "latency"),
			strings.Contains(l, "_bytes_total"), strings.Contains(l, "request_bytes"), strings.Contains(l, "response_bytes"),
			strings.Contains(l, "network"), strings.Contains(l, "receive"), strings.Contains(l, "transmit"):
			continue
		case strings.Contains(l, "cpu"):
			kind = MetricCPU
		case strings.Contains(l, "mem"):
			kind = MetricMemory
		case strings.Contains(l, "request"):
			kind = MetricRequests
		case strings.Contains(l, "instance_info"):
			kind = MetricInstances
		default:
			continue
		}
		if _, ok := out[kind]; !ok {
			out[kind] = n
		}
	}
	return out
}

// containerQueries builds the container query set from what the data
// source provides. CPU and memory are averaged over instances so they can be
// compared with the per-instance limits; requests are summed.
func containerQueries(info *SourceInfo, sel string) []Query {
	picks := PickContainerMetrics(info.Names())
	var qs []Query

	if cpu := picks[MetricCPU]; cpu != "" {
		if strings.Contains(cpu, "ratio") {
			// Despite its name, Scaleway reports this per instance already in
			// percent (observed values 1.9–13 on idle services).
			qs = append(qs, Query{MetricCPU, cpu, byID("avg", cpu+sel), UnitPercent})
		} else {
			qs = append(qs, Query{MetricCPU, cpu, byID("sum", rate(cpu, sel)), UnitCores})
		}
	}
	if mem := picks[MetricMemory]; mem != "" {
		if limit := info.has(ContainerMetricPrefix + "memory_limit_bytes"); limit != "" {
			qs = append(qs, Query{MetricMemory, mem,
				fmt.Sprintf("100 * %s / %s", byID("avg", mem+sel), byID("avg", limit+sel)), UnitPercent})
		} else {
			qs = append(qs, Query{MetricMemory, mem, byID("avg", mem+sel), unitFromName(mem)})
		}
	}

	if inst := picks[MetricInstances]; inst != "" {
		// One series per running instance. Containers scaled to zero have
		// no series; "or 0 * status" reports them as 0 instead of missing.
		q := byID("count", inst+sel)
		if status := info.has(ContainerMetricPrefix + "status"); status != "" {
			q += " or 0 * " + byID("max", status+sel)
		}
		qs = append(qs, Query{MetricInstances, inst, q, UnitCount})
	}

	if req := picks[MetricRequests]; req != "" {
		perSec := func(sel string) string {
			if strings.HasSuffix(req, "_total") {
				return byID("sum", rate(req, sel))
			}
			return byID("sum", req+sel) // already a rate
		}
		qs = append(qs, Query{MetricRequests, req, perSec(sel), UnitPerSec})
		if info.StatusLabel != "" {
			// "or 0 * all" keeps containers without errors at 0 % instead
			// of dropping them from the result.
			all := perSec(sel)
			errs := perSec(withMatcher(sel, info.StatusLabel+`=~"5.*"`))
			qs = append(qs, Query{MetricErrorRate, req,
				fmt.Sprintf("100 * (%s or 0 * %s) / %s", errs, all, all), UnitPercent})
		}
	}

	if bucket := picks[MetricLatency]; bucket != "" {
		q := fmt.Sprintf("histogram_quantile(0.95, sum by (%s, le) (%s))", resourceIDLabel, rate(bucket, sel))
		if strings.Contains(bucket, "milliseconds") {
			q += " / 1000"
		}
		qs = append(qs, Query{MetricLatency, bucket, q, UnitSeconds})
	}
	return qs
}

// --- databases ---

// DatabaseMetricPrefix returns the Cockpit metric prefix for a database
// engine such as "PostgreSQL-16" or "MySQL-8", or "" if unsupported.
func DatabaseMetricPrefix(engine string) string {
	name, _, _ := strings.Cut(strings.ToLower(engine), "-")
	switch name {
	case "postgresql", "mysql":
		return databaseMetricRoot + name + "_"
	}
	return ""
}

// databaseQueries builds the database query set. CPU, memory, disk and
// connections always run (their names are documented); the rest only when
// the data source has the metrics. HA instances report one series per
// node (told apart by the "instance" label), so the busiest node is shown.
func databaseQueries(prefix, sel string, info *SourceInfo) []Query {
	mysql := strings.Contains(prefix, "mysql")
	connections := prefix + "pg_stat_database_numbackends"
	if mysql {
		connections = prefix + "mysql_global_status_threads_connected"
	}
	idle := withMatcher(sel, `mode="idle"`)
	qs := []Query{
		{MetricCPU, prefix + "node_cpu_seconds_total",
			fmt.Sprintf(`100 * (1 - %s)`, byID("min", fmt.Sprintf("avg by (%s, instance) (%s)", resourceIDLabel, rate(prefix+"node_cpu_seconds_total", idle)))),
			UnitPercent},
		{MetricMemory, prefix + "node_memory_MemAvailable_bytes",
			byID("max", fmt.Sprintf("100 * (1 - %snode_memory_MemAvailable_bytes%s / %snode_memory_MemTotal_bytes%s)", prefix, sel, prefix, sel)),
			UnitPercent},
		{MetricDisk, prefix + "node_filesystem_avail_bytes",
			byID("max", fmt.Sprintf("100 * (1 - %snode_filesystem_avail_bytes%s / %snode_filesystem_size_bytes%s)", prefix, sel, prefix, sel)),
			UnitPercent},
		{MetricConnections, connections, byID("sum", connections+sel), UnitCount},
	}

	maxConn := info.has(prefix+"pg_settings_max_connections", prefix+"pg_settings_max_connection")
	lag := info.has(prefix + "pg_replication_lag")
	tps := info.has(prefix + "pg_stat_database_xact_commit")
	if mysql {
		maxConn = info.has(prefix + "mysql_global_variables_max_connections")
		lag = info.has(prefix + "mysql_slave_status_seconds_behind_master")
		tps = info.has(prefix+"mysql_global_status_queries", prefix+"mysql_global_status_questions")
	}
	if maxConn != "" {
		qs = append(qs, Query{MetricMaxConnections, maxConn, byID("max", maxConn+sel), UnitCount})
	}
	if lag != "" {
		qs = append(qs, Query{MetricReplicationLag, lag, byID("max", lag+sel), UnitSeconds})
	}
	if tps != "" {
		qs = append(qs, Query{MetricTransactions, tps, byID("sum", rate(tps, sel)), UnitPerSec})
	}

	if mysql {
		reads := info.has(prefix + "mysql_global_status_innodb_buffer_pool_reads")
		requests := info.has(prefix + "mysql_global_status_innodb_buffer_pool_read_requests")
		if reads != "" && requests != "" {
			qs = append(qs, Query{MetricCacheHit, requests,
				fmt.Sprintf("100 * (1 - %s / %s)", byID("sum", rate(reads, sel)), byID("sum", rate(requests, sel))), UnitPercent})
		}
	} else {
		hit := info.has(prefix + "pg_stat_database_blks_hit")
		read := info.has(prefix + "pg_stat_database_blks_read")
		if hit != "" && read != "" {
			h, r := byID("sum", rate(hit, sel)), byID("sum", rate(read, sel))
			qs = append(qs, Query{MetricCacheHit, hit, fmt.Sprintf("100 * %s / (%s + %s)", h, h, r), UnitPercent})
		}
		if wal := pickWAL(prefix, info); wal != "" {
			if strings.Contains(wal, "size") {
				qs = append(qs, Query{MetricWAL, wal, byID("max", wal+sel), UnitBytes})
			} else {
				qs = append(qs, Query{MetricWAL, wal, byID("sum", rate(wal, sel)), UnitBytesPS})
			}
		}
	}
	return qs
}

// pickWAL prefers a WAL bytes counter (write rate) over a WAL size gauge.
func pickWAL(prefix string, info *SourceInfo) string {
	var size string
	for _, n := range info.Names() {
		if !strings.HasPrefix(n, prefix) || !strings.Contains(n, "wal") || !strings.Contains(n, "bytes") {
			continue
		}
		if strings.Contains(n, "size") {
			if size == "" {
				size = n
			}
			continue
		}
		return n
	}
	return size
}

// PlannedQueries returns the queries scwpod would run in a data source for
// containers, Instances and each database engine, with placeholder IDs. Used by
// `scwpod metrics`.
func PlannedQueries(info *SourceInfo) map[string][]Query {
	sel := idSelector([]string{"<id>"})
	return map[string][]Query{
		"containers": containerQueries(info, sel),
		"instances":  instanceQueries(info, sel),
		"postgresql": databaseQueries(DatabaseMetricPrefix("postgresql"), sel, info),
		"mysql":      databaseQueries(DatabaseMetricPrefix("mysql"), sel, info),
	}
}

// --- instances ---

// InstanceMetricPrefix is the name prefix of Scaleway Instance metrics.
const InstanceMetricPrefix = "instance_"

// find returns the shortest discovered name with prefix that contains all
// of want and none of skip (case-insensitive), or "".
func (i *SourceInfo) find(prefix string, want, skip []string) string {
	best := ""
outer:
	for _, n := range i.Names() {
		l := strings.ToLower(n)
		if !strings.HasPrefix(l, prefix) {
			continue
		}
		for _, w := range want {
			if !strings.Contains(l, w) {
				continue outer
			}
		}
		for _, s := range skip {
			if strings.Contains(l, s) {
				continue outer
			}
		}
		if best == "" || len(n) < len(best) || (len(n) == len(best) && n < best) {
			best = n
		}
	}
	return best
}

// firstFound returns the first non-empty result of find over the
// candidate want lists.
func (i *SourceInfo) firstFound(prefix string, skip []string, wants ...[]string) string {
	for _, w := range wants {
		if n := i.find(prefix, w, skip); n != "" {
			return n
		}
	}
	return ""
}

// instanceCPU is the Instance CPU metric name, or "".
func (i *SourceInfo) instanceCPU() string {
	return i.find(InstanceMetricPrefix, []string{"cpu"}, []string{"limit", "steal", "throttl", "count", "info"})
}

// isCounter reports whether a metric name looks like a Prometheus counter.
func isCounter(name string) bool { return strings.HasSuffix(name, "_total") }

// perSecond sums a counter's rate by resource, or a gauge that already is
// a rate.
func perSecond(name, sel string) string {
	if isCounter(name) {
		return byID("sum", rate(name, sel))
	}
	return byID("sum", name+sel)
}

// instanceQueries builds the Instance query set from the names the data
// source provides. Scaleway does not document them, so they are matched
// loosely: "cpu", "mem" with available/used/total, disk or volume
// read/write bytes and network rx/tx bytes. CPU in cores and memory in bytes are
// turned into percentages by the UI using the Instance type's size.
//
// Observed with the Scaleway monitoring agent (instance_server_agent_up):
// instance_server_cpu_seconds_total, _vcpu_count, _memory_{available,
// free,used,total}, _io_{read,write}_bytes_total, _net_{rx,tx}_*, _swap_*.
// The agent reports no file system usage, so there is no disk usage.
func instanceQueries(info *SourceInfo, sel string) []Query {
	p := InstanceMetricPrefix
	var qs []Query

	if cpu := info.instanceCPU(); cpu != "" {
		switch {
		case isCounter(cpu):
			s := sel
			if info.InstanceCPUMode {
				s = withMatcher(sel, `mode!="idle"`)
			}
			busy := byID("sum", rate(cpu, s))
			if n := info.has(p + "server_vcpu_count"); n != "" {
				// The agent reports the vCPU count; without it the UI
				// uses the size of the Instance type.
				qs = append(qs, Query{MetricCPU, cpu, fmt.Sprintf("100 * %s / %s", busy, byID("max", n+sel)), UnitPercent})
			} else {
				qs = append(qs, Query{MetricCPU, cpu, busy, UnitCores})
			}
		case strings.Contains(cpu, "percent"), strings.Contains(cpu, "ratio"):
			qs = append(qs, Query{MetricCPU, cpu, byID("avg", cpu+sel), UnitPercent})
		default:
			qs = append(qs, Query{MetricCPU, cpu, byID("avg", cpu+sel), UnitCores})
		}
	}

	memSkip := []string{"swap", "limit", "info"}
	total := info.firstFound(p, memSkip, []string{"mem", "total"}, []string{"mem", "size"})
	avail := info.firstFound(p, memSkip, []string{"mem", "avail"}, []string{"mem", "free"})
	used := info.firstFound(p, append(memSkip, "total", "avail", "free", "size"), []string{"mem", "used"}, []string{"mem", "usage"})
	switch {
	case used != "" && (strings.Contains(used, "percent") || strings.Contains(used, "ratio")):
		qs = append(qs, Query{MetricMemory, used, byID("avg", used+sel), UnitPercent})
	case avail != "" && total != "":
		qs = append(qs, Query{MetricMemory, avail,
			fmt.Sprintf("100 * (1 - %s / %s)", byID("avg", avail+sel), byID("avg", total+sel)), UnitPercent})
	case used != "" && total != "":
		qs = append(qs, Query{MetricMemory, used,
			fmt.Sprintf("100 * %s / %s", byID("avg", used+sel), byID("avg", total+sel)), UnitPercent})
	case used != "":
		qs = append(qs, Query{MetricMemory, used, byID("avg", used+sel), unitFromName(used)})
	}

	// Volume throughput.
	ioSkip := []string{"network", "receive", "transmit", "ops", "request", "time", "seconds"}
	var read, write string
	for _, dev := range []string{"disk", "volume", "block", "_io_"} {
		if read == "" {
			read = info.find(p, []string{dev, "read", "bytes"}, ioSkip)
		}
		if write == "" {
			write = info.find(p, []string{dev, "writ", "bytes"}, ioSkip)
		}
	}
	if q, ok := bothWays(MetricDiskIO, read, write, sel); ok {
		qs = append(qs, q)
	}

	// Network traffic in + out, all of it (the agent also reports the
	// external, internet-facing part separately).
	netSkip := []string{"packets", "external", "drop", "err"}
	rx := info.firstFound(p, netSkip, []string{"net", "rx", "bytes"}, []string{"receive", "bytes"})
	tx := info.firstFound(p, netSkip, []string{"net", "tx", "bytes"}, []string{"transmit", "bytes"})
	if q, ok := bothWays(MetricNetwork, rx, tx, sel); ok {
		qs = append(qs, q)
	}
	return qs
}

// bothWays sums the per-second rates of two directions (read/write,
// in/out), keeping a resource that reports only one of them.
func bothWays(kind MetricKind, a, b, sel string) (Query, bool) {
	switch {
	case a != "" && b != "":
		ra, rb := perSecond(a, sel), perSecond(b, sel)
		return Query{kind, a, fmt.Sprintf("(%s or 0 * %s) + (%s or 0 * %s)", ra, rb, rb, ra), UnitBytesPS}, true
	case a != "" || b != "":
		return Query{kind, a + b, perSecond(a+b, sel), UnitBytesPS}, true
	}
	return Query{}, false
}
