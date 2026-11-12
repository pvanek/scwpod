package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func infoWith(statusLabel string, names ...string) *SourceInfo {
	i := &SourceInfo{names: map[string]bool{}, StatusLabel: statusLabel}
	for _, n := range names {
		i.names[n] = true
	}
	return i
}

func queriesByKind(qs []Query) map[MetricKind]Query {
	out := map[MetricKind]Query{}
	for _, q := range qs {
		out[q.Kind] = q
	}
	return out
}

// scalewayMetrics are the names a real Cockpit data source reported
// (`scwpod metrics`, October 2026).
var scalewayMetrics = []string{
	"rdb_instance_postgresql_node_cpu_seconds_total",
	"rdb_instance_postgresql_node_disk_read_bytes_total",
	"rdb_instance_postgresql_node_disk_reads_completed_total",
	"rdb_instance_postgresql_node_disk_writes_completed_total",
	"rdb_instance_postgresql_node_disk_written_bytes_total",
	"rdb_instance_postgresql_node_filesystem_avail_bytes",
	"rdb_instance_postgresql_node_filesystem_free_bytes",
	"rdb_instance_postgresql_node_filesystem_size_bytes",
	"rdb_instance_postgresql_node_memory_MemAvailable_bytes",
	"rdb_instance_postgresql_node_memory_MemTotal_bytes",
	"rdb_instance_postgresql_pg_replication_lag",
	"rdb_instance_postgresql_pg_settings_max_connections",
	"rdb_instance_postgresql_pg_stat_activity_count",
	"rdb_instance_postgresql_pg_stat_database_numbackends",
	"serverless_container_cpu_usage_ratio",
	"serverless_container_info",
	"serverless_container_instance_info",
	"serverless_container_instances_total",
	"serverless_container_memory_limit_bytes",
	"serverless_container_memory_usage_bytes",
	"serverless_container_receive_bytes_total",
	"serverless_container_requests_per_second",
	"serverless_container_status",
	"serverless_container_transmit_bytes_total",
}

func TestPickContainerMetricsScaleway(t *testing.T) {
	got := PickContainerMetrics(scalewayMetrics)
	want := map[MetricKind]string{
		MetricCPU:       "serverless_container_cpu_usage_ratio",
		MetricMemory:    "serverless_container_memory_usage_bytes",
		MetricRequests:  "serverless_container_requests_per_second",
		MetricInstances: "serverless_container_instance_info",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestPickContainerMetricsHistogram(t *testing.T) {
	got := PickContainerMetrics([]string{
		"serverless_container_cpu_usage_seconds_total",
		"serverless_container_request_duration_seconds_bucket",
		"serverless_container_request_duration_seconds_sum",
		"serverless_container_requests_total",
	})
	if got[MetricLatency] != "serverless_container_request_duration_seconds_bucket" ||
		got[MetricRequests] != "serverless_container_requests_total" {
		t.Errorf("got %v", got)
	}
}

func TestContainerQueriesScaleway(t *testing.T) {
	got := queriesByKind(containerQueries(infoWith("status", scalewayMetrics...), `{resource_id=~"a"}`))
	want := map[MetricKind]Query{
		MetricCPU: {MetricCPU, "serverless_container_cpu_usage_ratio",
			`avg by (resource_id) (serverless_container_cpu_usage_ratio{resource_id=~"a"})`, UnitPercent},
		MetricMemory: {MetricMemory, "serverless_container_memory_usage_bytes",
			`100 * avg by (resource_id) (serverless_container_memory_usage_bytes{resource_id=~"a"}) / avg by (resource_id) (serverless_container_memory_limit_bytes{resource_id=~"a"})`, UnitPercent},
		MetricInstances: {MetricInstances, "serverless_container_instance_info",
			`count by (resource_id) (serverless_container_instance_info{resource_id=~"a"}) or 0 * max by (resource_id) (serverless_container_status{resource_id=~"a"})`, UnitCount},
		MetricRequests: {MetricRequests, "serverless_container_requests_per_second",
			`sum by (resource_id) (serverless_container_requests_per_second{resource_id=~"a"})`, UnitPerSec},
		MetricErrorRate: {MetricErrorRate, "serverless_container_requests_per_second",
			`100 * (sum by (resource_id) (serverless_container_requests_per_second{resource_id=~"a",status=~"5.*"}) or 0 * sum by (resource_id) (serverless_container_requests_per_second{resource_id=~"a"})) / sum by (resource_id) (serverless_container_requests_per_second{resource_id=~"a"})`, UnitPercent},
	}
	if len(got) != len(want) {
		t.Errorf("got %d queries, want %d", len(got), len(want))
	}
	for kind, w := range want {
		if got[kind] != w {
			t.Errorf("%s:\n got %+v\nwant %+v", kind, got[kind], w)
		}
	}
}

func TestContainerQueriesCounters(t *testing.T) {
	info := infoWith("code",
		"serverless_container_cpu_usage_seconds_total",
		"serverless_container_requests_total",
		"serverless_container_request_duration_seconds_bucket",
	)
	got := queriesByKind(containerQueries(info, `{resource_id=~"a"}`))
	if q := got[MetricCPU]; q.Unit != UnitCores || q.PromQL != `sum by (resource_id) (rate(serverless_container_cpu_usage_seconds_total{resource_id=~"a"}[5m]))` {
		t.Errorf("cpu: %+v", q)
	}
	if q := got[MetricRequests]; q.PromQL != `sum by (resource_id) (rate(serverless_container_requests_total{resource_id=~"a"}[5m]))` {
		t.Errorf("requests: %+v", q)
	}
	if q := got[MetricLatency]; q.PromQL != `histogram_quantile(0.95, sum by (resource_id, le) (rate(serverless_container_request_duration_seconds_bucket{resource_id=~"a"}[5m])))` {
		t.Errorf("latency: %+v", q)
	}
	info.StatusLabel = ""
	if _, ok := queriesByKind(containerQueries(info, `{resource_id=~"a"}`))[MetricErrorRate]; ok {
		t.Error("error rate without status label")
	}
}

func TestDatabaseMetricPrefix(t *testing.T) {
	for engine, want := range map[string]string{
		"PostgreSQL-16": "rdb_instance_postgresql_",
		"MySQL-8":       "rdb_instance_mysql_",
		"Redis-7":       "",
	} {
		if got := DatabaseMetricPrefix(engine); got != want {
			t.Errorf("%s: got %q, want %q", engine, got, want)
		}
	}
}

func TestDatabaseQueriesRequiredOnly(t *testing.T) {
	got := queriesByKind(databaseQueries("rdb_instance_postgresql_", `{resource_id=~"d1"}`, nil))
	want := map[MetricKind]string{
		MetricCPU:         `100 * (1 - min by (resource_id) (avg by (resource_id, instance) (rate(rdb_instance_postgresql_node_cpu_seconds_total{resource_id=~"d1",mode="idle"}[5m]))))`,
		MetricMemory:      `max by (resource_id) (100 * (1 - rdb_instance_postgresql_node_memory_MemAvailable_bytes{resource_id=~"d1"} / rdb_instance_postgresql_node_memory_MemTotal_bytes{resource_id=~"d1"}))`,
		MetricDisk:        `max by (resource_id) (100 * (1 - rdb_instance_postgresql_node_filesystem_avail_bytes{resource_id=~"d1"} / rdb_instance_postgresql_node_filesystem_size_bytes{resource_id=~"d1"}))`,
		MetricConnections: `sum by (resource_id) (rdb_instance_postgresql_pg_stat_database_numbackends{resource_id=~"d1"})`,
	}
	if len(got) != len(want) {
		t.Errorf("got %d queries, want %d: %v", len(got), len(want), got)
	}
	for kind, w := range want {
		if got[kind].PromQL != w {
			t.Errorf("%s:\n got %s\nwant %s", kind, got[kind].PromQL, w)
		}
	}
}

func TestDatabaseQueriesOptional(t *testing.T) {
	p := "rdb_instance_postgresql_"
	info := infoWith("",
		p+"pg_settings_max_connections", p+"pg_replication_lag",
		p+"pg_stat_database_xact_commit_total", p+"pg_stat_database_blks_hit", p+"pg_stat_database_blks_read",
		p+"pg_wal_size_bytes", p+"pg_stat_wal_wal_bytes",
	)
	got := queriesByKind(databaseQueries(p, `{resource_id=~"d1"}`, info))
	checks := map[MetricKind]string{
		MetricMaxConnections: `max by (resource_id) (rdb_instance_postgresql_pg_settings_max_connections{resource_id=~"d1"})`,
		MetricReplicationLag: `max by (resource_id) (rdb_instance_postgresql_pg_replication_lag{resource_id=~"d1"})`,
		MetricTransactions:   `sum by (resource_id) (rate(rdb_instance_postgresql_pg_stat_database_xact_commit_total{resource_id=~"d1"}[5m]))`,
		MetricWAL:            `sum by (resource_id) (rate(rdb_instance_postgresql_pg_stat_wal_wal_bytes{resource_id=~"d1"}[5m]))`,
	}
	for kind, w := range checks {
		if got[kind].PromQL != w {
			t.Errorf("%s:\n got %s\nwant %s", kind, got[kind].PromQL, w)
		}
	}
	if got[MetricWAL].Unit != UnitBytesPS || got[MetricReplicationLag].Unit != UnitSeconds {
		t.Errorf("units: wal %q lag %q", got[MetricWAL].Unit, got[MetricReplicationLag].Unit)
	}
	if _, ok := got[MetricCacheHit]; !ok {
		t.Error("no cache hit query")
	}

	m := "rdb_instance_mysql_"
	mq := queriesByKind(databaseQueries(m, `{resource_id=~"d1"}`, infoWith("",
		m+"mysql_slave_status_seconds_behind_master", m+"mysql_global_status_queries")))
	if mq[MetricConnections].Source != m+"mysql_global_status_threads_connected" ||
		mq[MetricReplicationLag].Source != m+"mysql_slave_status_seconds_behind_master" ||
		mq[MetricTransactions].Source != m+"mysql_global_status_queries" {
		t.Errorf("mysql queries: %+v", mq)
	}
	if _, ok := mq[MetricWAL]; ok {
		t.Error("WAL query for MySQL")
	}
}

func TestDownsample(t *testing.T) {
	if got := Downsample([]float64{1, 3, 5, 7}, 2); !reflect.DeepEqual(got, []float64{2, 6}) {
		t.Errorf("got %v", got)
	}
	if got := Downsample([]float64{1, 2}, 5); len(got) != 2 {
		t.Errorf("short input changed: %v", got)
	}
}

func TestPromClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != "secret" {
			http.Error(w, `{"status":"error","error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/prometheus/api/v1/label/__name__/values":
			w.Write([]byte(`{"status":"success","data":["serverless_container_requests_total"]}`))
		case "/prometheus/api/v1/query_range":
			w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
				{"metric":{"resource_id":"c1"},"values":[[1700000000,"0.5"],[1700000030,"NaN"],[1700000060,"1.5"]]}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	p := &promClient{base: srv.URL + "/prometheus", token: "secret"}
	names, err := p.labelValues(ctx, "__name__", `{__name__=~"x.+"}`)
	if err != nil || len(names) != 1 {
		t.Fatalf("labelValues = %v, %v", names, err)
	}
	series, err := p.queryRange(ctx, "q", time.Unix(0, 0), time.Unix(60, 0), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || series[0].labels["resource_id"] != "c1" || len(series[0].points) != 2 || series[0].points[1].V != 1.5 {
		t.Errorf("series = %+v", series)
	}

	p.token = "wrong"
	if _, err := p.labelValues(ctx, "__name__", "{}"); err == nil {
		t.Error("expected auth error")
	}
}

type noTokens struct{}

func (noTokens) Token(ProjectRegion) string           { return "" }
func (noTokens) SetToken(ProjectRegion, string) error { return nil }

func TestMetricsReportsMissingTokensForDatabases(t *testing.T) {
	s := &Service{info: map[string]*SourceInfo{}}
	got, err := s.Metrics(context.Background(),
		[]Container{{ID: "c1", ProjectID: "p1", Region: "fr-par"}},
		[]Database{
			{ID: "d1", ProjectID: "p2", Region: "nl-ams", Engine: "PostgreSQL-16", Status: "ready"},
			{ID: "d2", ProjectID: "p3", Region: "fr-par", Engine: "Redis-7", Status: "ready"},
		},
		[]Instance{{ID: "i1", ProjectID: "p4", Zone: "pl-waw-2"}},
		noTokens{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := []ProjectRegion{{"p1", "fr-par"}, {"p2", "nl-ams"}, {"p4", "pl-waw"}}
	if !reflect.DeepEqual(got.MissingTokens, want) {
		t.Errorf("missing = %v, want %v", got.MissingTokens, want)
	}
}

func TestContainerMemoryWithoutLimitMetric(t *testing.T) {
	info := infoWith("", "serverless_container_memory_usage_bytes")
	q := queriesByKind(containerQueries(info, `{resource_id=~"a"}`))[MetricMemory]
	// The UI converts bytes to % of the container's configured memory.
	if q.Unit != UnitBytes || q.PromQL != `avg by (resource_id) (serverless_container_memory_usage_bytes{resource_id=~"a"})` {
		t.Errorf("memory without limit metric: %+v", q)
	}
}

func TestContainerLinks(t *testing.T) {
	l := ContainerLinks("https://g.example", "fr-par", "ns123-api")
	for _, u := range []string{l.Metrics, l.Logs} {
		parsed, err := url.Parse(u)
		if err != nil {
			t.Fatal(err)
		}
		q := parsed.Query()
		if q.Get("var-region") != "fr-par" || q.Get("var-container_name") != "ns123-api" || q.Get("from") != "now-1h" {
			t.Errorf("query of %s = %v", u, q)
		}
	}
	if !strings.HasPrefix(l.Metrics, "https://g.example/d/scw-serverless-containers-overview/") ||
		!strings.HasPrefix(l.Logs, "https://g.example/d/scw-serverless-containers-logs/") {
		t.Errorf("links %+v", l)
	}
	if l := ContainerLinks("https://g.example", "fr-par", ""); strings.Contains(l.Metrics, "container_name") {
		t.Errorf("unknown name should leave the dashboard unfiltered: %s", l.Metrics)
	}
	if l := ContainerLinks("", "fr-par", "x"); l != (Links{}) {
		t.Errorf("no Grafana URL should give no links: %+v", l)
	}
}

func TestDatabaseLinks(t *testing.T) {
	l := DatabaseLinks("https://g.example", "fr-par", "PostgreSQL-16", "shop-dev-pg")
	if !strings.HasPrefix(l.Metrics, "https://g.example/d/scw-rdb-postgresql-overview/") || !strings.Contains(l.Metrics, "var-resource_name=shop-dev-pg") {
		t.Errorf("metrics link %s", l.Metrics)
	}
	if m := DatabaseLinks("https://g.example", "fr-par", "MySQL-8", "db"); !strings.Contains(m.Metrics, "scw-rdb-mysql-overview") {
		t.Errorf("mysql link %s", m.Metrics)
	}
	parsed, err := url.Parse(l.Logs)
	if err != nil {
		t.Fatal(err)
	}
	var left struct {
		Datasource string                  `json:"datasource"`
		Queries    []struct{ Expr string } `json:"queries"`
	}
	if err := json.Unmarshal([]byte(parsed.Query().Get("left")), &left); err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/explore" || left.Datasource != "Scaleway Logs" || left.Queries[0].Expr != `{resource_name="shop-dev-pg"}` {
		t.Errorf("logs link %s -> %+v", l.Logs, left)
	}
}

func TestInstanceQueriesCounters(t *testing.T) {
	info := infoWith("",
		"instance_server_cpu_seconds_total",
		"instance_server_memory_available_bytes", "instance_server_memory_total_bytes",
		"instance_server_disk_read_bytes_total", "instance_server_disk_write_bytes_total",
		"instance_server_disk_read_ops_total",
		"instance_server_network_receive_bytes_total",
		"rdb_instance_postgresql_node_cpu_seconds_total",
	)
	sel := `{resource_id=~"i1"}`
	qs := queriesByKind(instanceQueries(info, sel))
	want := map[MetricKind]Query{
		MetricCPU: {MetricCPU, "instance_server_cpu_seconds_total",
			`sum by (resource_id) (rate(instance_server_cpu_seconds_total{resource_id=~"i1"}[5m]))`, UnitCores},
		MetricMemory: {MetricMemory, "instance_server_memory_available_bytes",
			`100 * (1 - avg by (resource_id) (instance_server_memory_available_bytes{resource_id=~"i1"}) / avg by (resource_id) (instance_server_memory_total_bytes{resource_id=~"i1"}))`, UnitPercent},
	}
	for k, w := range want {
		if qs[k] != w {
			t.Errorf("%s:\n got %+v\nwant %+v", k, qs[k], w)
		}
	}
	if _, ok := qs[MetricDisk]; ok {
		t.Errorf("instances have no disk usage: %+v", qs[MetricDisk])
	}
	io := qs[MetricDiskIO]
	if io.Unit != UnitBytesPS || !strings.Contains(io.PromQL, "rate(instance_server_disk_read_bytes_total") ||
		!strings.Contains(io.PromQL, "rate(instance_server_disk_write_bytes_total") || strings.Contains(io.PromQL, "ops") {
		t.Errorf("disk io = %+v", io)
	}

	info.InstanceCPUMode = true
	if q := queriesByKind(instanceQueries(info, sel))[MetricCPU]; !strings.Contains(q.PromQL, `mode!="idle"`) {
		t.Errorf("cpu with mode label = %s", q.PromQL)
	}
}

func TestInstanceQueriesGauges(t *testing.T) {
	info := infoWith("",
		"instance_cpu_usage_percent",
		"instance_memory_used_bytes",
	)
	qs := queriesByKind(instanceQueries(info, `{resource_id=~"i1"}`))
	if q := qs[MetricCPU]; q.Unit != UnitPercent || q.Source != "instance_cpu_usage_percent" {
		t.Errorf("cpu = %+v", q)
	}
	// Bytes without a total: the UI converts with the type's RAM.
	if q := qs[MetricMemory]; q.Unit != UnitBytes || q.Source != "instance_memory_used_bytes" {
		t.Errorf("memory = %+v", q)
	}
	if len(instanceQueries(infoWith(""), "{}")) != 0 {
		t.Error("queries without any Instance metrics")
	}
}

// Names reported by the Scaleway monitoring agent (November 2026).
func TestInstanceQueriesScalewayAgent(t *testing.T) {
	info := infoWith("",
		"instance_server_agent_up", "instance_server_cpu_seconds_total",
		"instance_server_io_read_bytes_total", "instance_server_io_write_bytes_total",
		"instance_server_memory_available", "instance_server_memory_free",
		"instance_server_memory_total", "instance_server_memory_used",
		"instance_server_net_rx_bytes_total", "instance_server_net_tx_bytes_total",
		"instance_server_net_rx_external_bytes_total", "instance_server_net_rx_packets_total",
		"instance_server_swap_free", "instance_server_swap_total", "instance_server_swap_used",
		"instance_server_vcpu_count",
	)
	sel := `{resource_id=~"i1"}`
	qs := queriesByKind(instanceQueries(info, sel))
	want := map[MetricKind]Query{
		MetricCPU: {MetricCPU, "instance_server_cpu_seconds_total",
			`100 * sum by (resource_id) (rate(instance_server_cpu_seconds_total{resource_id=~"i1"}[5m])) / max by (resource_id) (instance_server_vcpu_count{resource_id=~"i1"})`, UnitPercent},
		MetricMemory: {MetricMemory, "instance_server_memory_available",
			`100 * (1 - avg by (resource_id) (instance_server_memory_available{resource_id=~"i1"}) / avg by (resource_id) (instance_server_memory_total{resource_id=~"i1"}))`, UnitPercent},
		MetricDiskIO: {MetricDiskIO, "instance_server_io_read_bytes_total",
			`(sum by (resource_id) (rate(instance_server_io_read_bytes_total{resource_id=~"i1"}[5m])) or 0 * sum by (resource_id) (rate(instance_server_io_write_bytes_total{resource_id=~"i1"}[5m]))) + (sum by (resource_id) (rate(instance_server_io_write_bytes_total{resource_id=~"i1"}[5m])) or 0 * sum by (resource_id) (rate(instance_server_io_read_bytes_total{resource_id=~"i1"}[5m])))`, UnitBytesPS},
	}
	want[MetricNetwork] = Query{MetricNetwork, "instance_server_net_rx_bytes_total",
		`(sum by (resource_id) (rate(instance_server_net_rx_bytes_total{resource_id=~"i1"}[5m])) or 0 * sum by (resource_id) (rate(instance_server_net_tx_bytes_total{resource_id=~"i1"}[5m]))) + (sum by (resource_id) (rate(instance_server_net_tx_bytes_total{resource_id=~"i1"}[5m])) or 0 * sum by (resource_id) (rate(instance_server_net_rx_bytes_total{resource_id=~"i1"}[5m])))`, UnitBytesPS}
	if len(qs) != len(want) {
		t.Errorf("got %d queries, want %d: %+v", len(qs), len(want), qs)
	}
	for k, w := range want {
		if qs[k] != w {
			t.Errorf("%s:\n got %+v\nwant %+v", k, qs[k], w)
		}
	}
}
