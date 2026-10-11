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
	fetched     time.Time
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
// containers and for each database engine, with placeholder IDs. Used by
// `scwpod metrics`.
func PlannedQueries(info *SourceInfo) map[string][]Query {
	sel := idSelector([]string{"<id>"})
	return map[string][]Query{
		"containers": containerQueries(info, sel),
		"postgresql": databaseQueries(DatabaseMetricPrefix("postgresql"), sel, info),
		"mysql":      databaseQueries(DatabaseMetricPrefix("mysql"), sel, info),
	}
}
