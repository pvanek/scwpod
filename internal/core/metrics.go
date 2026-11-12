package core

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// MetricsWindow is how much history the dashboard fetches.
const MetricsWindow = time.Hour

// MetricKind is what a metric measures, independent of its source name.
type MetricKind string

const (
	// Shared by containers and databases.
	MetricCPU    MetricKind = "cpu"    // percent of limit/capacity, or cores
	MetricMemory MetricKind = "memory" // percent of limit/capacity, or bytes

	// Containers.
	MetricInstances MetricKind = "instances"  // running instances
	MetricRequests  MetricKind = "requests"   // requests per second
	MetricErrorRate MetricKind = "error_rate" // percent of requests answered 5xx
	MetricLatency   MetricKind = "latency"    // p95 response time

	// Databases.
	MetricDisk           MetricKind = "disk"            // percent used
	MetricConnections    MetricKind = "connections"     // open connections
	MetricMaxConnections MetricKind = "max_connections" // configured connection limit
	MetricReplicationLag MetricKind = "replication_lag" // seconds behind primary
	MetricWAL            MetricKind = "wal"             // WAL size or write rate
	MetricTransactions   MetricKind = "transactions"    // commits (PostgreSQL) or queries (MySQL) per second
	MetricCacheHit       MetricKind = "cache_hit"       // percent of reads served from cache

	// Instances (virtual machines).
	MetricDiskIO  MetricKind = "disk_io" // volume read + write throughput
	MetricNetwork MetricKind = "network" // received + sent throughput
)

// Unit tells the UI how to format a metric value.
type Unit string

const (
	UnitPercent Unit = "%"
	UnitBytes   Unit = "bytes"
	UnitCores   Unit = "cores"
	UnitPerSec  Unit = "/s"
	UnitSeconds Unit = "s"
	UnitBytesPS Unit = "bytes/s"
	UnitCount   Unit = ""
)

// Point is one sample of a time series.
type Point struct {
	T time.Time
	V float64
}

// Metric is one time series of a resource, oldest point first.
type Metric struct {
	Source string // name of the metric at the source, for debugging
	Unit   Unit
	Points []Point
}

// Last returns the most recent value, or false if there are no points.
func (m Metric) Last() (float64, bool) {
	if len(m.Points) == 0 {
		return 0, false
	}
	return m.Points[len(m.Points)-1].V, true
}

// Values returns the point values, oldest first.
func (m Metric) Values() []float64 {
	out := make([]float64, len(m.Points))
	for i, p := range m.Points {
		out[i] = p.V
	}
	return out
}

// ResourceMetrics holds the metrics of one resource, keyed by kind.
type ResourceMetrics map[MetricKind]Metric

func unitFromName(name string) Unit {
	switch {
	case strings.Contains(name, "percent"):
		return UnitPercent
	case strings.Contains(name, "bytes"):
		return UnitBytes
	default:
		return UnitCount
	}
}

// Downsample averages values into n buckets, keeping their order. Fewer
// values than n are returned as is.
func Downsample(values []float64, n int) []float64 {
	if n <= 0 || len(values) <= n {
		return values
	}
	out := make([]float64, n)
	for i := range n {
		lo, hi := i*len(values)/n, (i+1)*len(values)/n
		var sum float64
		for _, v := range values[lo:hi] {
			sum += v
		}
		out[i] = sum / float64(hi-lo)
	}
	return out
}

func joinErrs(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Error()
	}
	sort.Strings(msgs)
	return fmt.Errorf("%s", strings.Join(msgs, "; "))
}
