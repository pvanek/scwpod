package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	cockpit "github.com/scaleway/scaleway-sdk-go/api/cockpit/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

// Container and database metrics come from Cockpit, Scaleway's
// Prometheus-compatible observability service. Querying it needs a Cockpit
// token per project and region; those are stored outside core via
// CockpitTokens.

// resourceIDLabel is the label carrying the container or database ID on
// Scaleway-provided metrics.
const resourceIDLabel = "resource_id"

// ProjectRegion identifies one Cockpit scope.
type ProjectRegion struct {
	ProjectID string
	Region    string
}

// CockpitTokens stores Cockpit query tokens.
type CockpitTokens interface {
	Token(pr ProjectRegion) string
	SetToken(pr ProjectRegion, secret string) error
}

// Metrics is the result of a metrics fetch, keyed by container, database
// or Instance ID.
type Metrics struct {
	ByResource    map[string]ResourceMetrics
	MissingTokens []ProjectRegion // scopes with resources but no token
	// Names maps container IDs to Cockpit's name for them (the
	// resource_name label), which Grafana dashboards filter by.
	Names map[string]string
}

// group is the set of resources sharing one Cockpit scope.
type group struct {
	containerIDs []string
	instanceIDs  []string
	dbsByEngine  map[string][]string // engine metric prefix -> database IDs
}

// Metrics fetches the last MetricsWindow of metrics for containers,
// databases and Instances from Cockpit, one query set per project and
// region (an Instance's zone belongs to a region).
func (s *Service) Metrics(ctx context.Context, containers []Container, dbs []Database, instances []Instance, tokens CockpitTokens, now time.Time) (Metrics, error) {
	groups := map[ProjectRegion]*group{}
	get := func(pr ProjectRegion) *group {
		if groups[pr] == nil {
			groups[pr] = &group{dbsByEngine: map[string][]string{}}
		}
		return groups[pr]
	}
	for _, c := range containers {
		g := get(ProjectRegion{c.ProjectID, c.Region})
		g.containerIDs = append(g.containerIDs, c.ID)
	}
	for _, d := range dbs {
		prefix := DatabaseMetricPrefix(d.Engine)
		if prefix == "" || d.Status != "ready" {
			continue
		}
		g := get(ProjectRegion{d.ProjectID, d.Region})
		g.dbsByEngine[prefix] = append(g.dbsByEngine[prefix], d.ID)
	}

	for _, in := range instances {
		region, err := scw.Zone(in.Zone).Region()
		if err != nil {
			continue
		}
		g := get(ProjectRegion{in.ProjectID, string(region)})
		g.instanceIDs = append(g.instanceIDs, in.ID)
	}

	out := Metrics{ByResource: map[string]ResourceMetrics{}, Names: map[string]string{}}
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		errs []error
	)
	for pr, g := range groups {
		token := tokens.Token(pr)
		if token == "" {
			out.MissingTokens = append(out.MissingTokens, pr)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, names, err := s.groupMetrics(ctx, pr, token, g, now)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("metrics in %s: %w", pr.Region, err))
			}
			for id, rm := range res {
				out.ByResource[id] = rm
			}
			for id, n := range names {
				out.Names[id] = n
			}
		}()
	}
	wg.Wait()
	sort.Slice(out.MissingTokens, func(i, j int) bool {
		a, b := out.MissingTokens[i], out.MissingTokens[j]
		return a.ProjectID+a.Region < b.ProjectID+b.Region
	})
	return out, joinErrs(errs)
}

func (s *Service) groupMetrics(ctx context.Context, pr ProjectRegion, token string, g *group, now time.Time) (map[string]ResourceMetrics, map[string]string, error) {
	prom, err := s.prometheus(ctx, pr, token)
	if err != nil {
		return nil, nil, err
	}
	var errs []error
	info, err := s.sourceInfo(ctx, prom, now)
	if err != nil {
		// Without discovery only the fixed database queries can run.
		errs = append(errs, err)
	}

	var queries []Query
	if len(g.containerIDs) > 0 {
		queries = append(queries, containerQueries(info, idSelector(g.containerIDs))...)
	}
	if len(g.instanceIDs) > 0 {
		queries = append(queries, instanceQueries(info, idSelector(g.instanceIDs))...)
	}
	for prefix, ids := range g.dbsByEngine {
		queries = append(queries, databaseQueries(prefix, idSelector(ids), info)...)
	}

	out := map[string]ResourceMetrics{}
	for _, q := range queries {
		series, err := prom.queryRange(ctx, q.PromQL, now.Add(-MetricsWindow), now, time.Minute)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", q.Source, err))
			continue
		}
		for _, sr := range series {
			id := sr.labels[resourceIDLabel]
			if out[id] == nil {
				out[id] = ResourceMetrics{}
			}
			out[id][q.Kind] = Metric{Source: q.Source, Unit: q.Unit, Points: sr.points}
		}
	}

	names := map[string]string{}
	if infoMetric := info.has(ContainerMetricPrefix + "info"); infoMetric != "" && len(g.containerIDs) > 0 {
		q := fmt.Sprintf("group by (%s, resource_name) (%s%s)", resourceIDLabel, infoMetric, idSelector(g.containerIDs))
		if series, err := prom.query(ctx, q); err == nil {
			for _, sr := range series {
				names[sr.labels[resourceIDLabel]] = sr.labels["resource_name"]
			}
		}
	}
	return out, names, joinErrs(errs)
}

func idSelector(ids []string) string {
	return fmt.Sprintf(`{%s=~"%s"}`, resourceIDLabel, strings.Join(ids, "|"))
}

// sourceInfoTTL is how long discovered metric names are reused.
const sourceInfoTTL = 10 * time.Minute

// sourceInfo returns the Scaleway metric names available in a data source
// and the label carrying the HTTP status on container requests.
func (s *Service) sourceInfo(ctx context.Context, p *promClient, now time.Time) (*SourceInfo, error) {
	s.infoMu.Lock()
	cached := s.info[p.base]
	s.infoMu.Unlock()
	if cached != nil && now.Sub(cached.fetched) < sourceInfoTTL {
		return cached, nil
	}

	info := &SourceInfo{names: map[string]bool{}, fetched: now}
	for _, match := range []string{
		fmt.Sprintf(`{__name__=~"%s.+"}`, ContainerMetricPrefix),
		fmt.Sprintf(`{__name__=~"%s.+"}`, databaseMetricRoot),
		fmt.Sprintf(`{__name__=~"%s.+"}`, InstanceMetricPrefix),
	} {
		names, err := p.labelValues(ctx, "__name__", match)
		if err != nil {
			return info, fmt.Errorf("discovering metrics: %w", err)
		}
		for _, n := range names {
			info.names[n] = true
		}
	}
	if req := PickContainerMetrics(info.Names())[MetricRequests]; req != "" {
		if series, err := p.query(ctx, "topk(1, "+req+")"); err == nil && len(series) > 0 {
			info.StatusLabel = statusLabel(series[0].labels)
		}
	}

	if cpu := info.instanceCPU(); cpu != "" && isCounter(cpu) {
		if series, err := p.query(ctx, "topk(1, "+cpu+")"); err == nil && len(series) > 0 {
			_, info.InstanceCPUMode = series[0].labels["mode"]
		}
	}

	s.infoMu.Lock()
	s.info[p.base] = info
	s.infoMu.Unlock()
	return info, nil
}

// --- Cockpit / Prometheus plumbing ---

type promClient struct {
	base  string // ends with /prometheus
	token string
}

type promSeries struct {
	labels map[string]string
	points []Point
}

// prometheus returns a client for the Scaleway metrics data source of pr.
func (s *Service) prometheus(ctx context.Context, pr ProjectRegion, token string) (*promClient, error) {
	resp, err := s.cockpit.ListDataSources(&cockpit.RegionalAPIListDataSourcesRequest{
		Region:    scw.Region(pr.Region),
		ProjectID: pr.ProjectID,
		Origin:    cockpit.DataSourceOriginScaleway,
		Types:     []cockpit.DataSourceType{cockpit.DataSourceTypeMetrics},
	}, scw.WithContext(ctx), scw.WithAllPages())
	if err != nil {
		return nil, fmt.Errorf("listing Cockpit data sources: %w", err)
	}
	if len(resp.DataSources) == 0 {
		return nil, fmt.Errorf("no Scaleway metrics data source in project %s", pr.ProjectID)
	}
	base := strings.TrimRight(resp.DataSources[0].URL, "/")
	if !strings.HasSuffix(base, "/prometheus") {
		base += "/prometheus"
	}
	return &promClient{base: base, token: token}, nil
}

func (p *promClient) get(ctx context.Context, path string, params url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+path+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Token", p.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("cockpit %s: %s %s", path, resp.Status, msg)
	}
	var env struct {
		Status string          `json:"status"`
		Error  string          `json:"error"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("cockpit %s: decoding response: %w", path, err)
	}
	if env.Status != "success" {
		return fmt.Errorf("cockpit %s: %s", path, env.Error)
	}
	return json.Unmarshal(env.Data, out)
}

func (p *promClient) labelValues(ctx context.Context, label, match string) ([]string, error) {
	var out []string
	params := url.Values{"match[]": {match}}
	err := p.get(ctx, "/api/v1/label/"+label+"/values", params, &out)
	return out, err
}

func (p *promClient) queryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]promSeries, error) {
	var data struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Values [][2]any          `json:"values"`
		} `json:"result"`
	}
	params := url.Values{
		"query": {query},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {strconv.Itoa(int(step.Seconds()))},
	}
	if err := p.get(ctx, "/api/v1/query_range", params, &data); err != nil {
		return nil, err
	}
	out := make([]promSeries, 0, len(data.Result))
	for _, r := range data.Result {
		sr := promSeries{labels: r.Metric}
		for _, v := range r.Values {
			ts, ok1 := v[0].(float64)
			vs, ok2 := v[1].(string)
			if !ok1 || !ok2 {
				continue
			}
			f, err := strconv.ParseFloat(vs, 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				continue
			}
			sr.points = append(sr.points, Point{T: time.Unix(int64(ts), 0), V: f})
		}
		out = append(out, sr)
	}
	return out, nil
}

// CreateCockpitTokens creates a read-only metrics token for each scope and
// stores it. It modifies the account, so callers must ask the user first.
func (s *Service) CreateCockpitTokens(ctx context.Context, scopes []ProjectRegion, tokens CockpitTokens) error {
	for _, pr := range scopes {
		tok, err := s.cockpit.CreateToken(&cockpit.RegionalAPICreateTokenRequest{
			Region:      scw.Region(pr.Region),
			ProjectID:   pr.ProjectID,
			Name:        "scwpod-read-metrics",
			TokenScopes: []cockpit.TokenScope{cockpit.TokenScopeReadOnlyMetrics},
		}, scw.WithContext(ctx))
		if err != nil {
			return fmt.Errorf("creating Cockpit token in %s: %w", pr.Region, err)
		}
		if tok.SecretKey == nil {
			return fmt.Errorf("cockpit token in %s was created without a secret", pr.Region)
		}
		if err := tokens.SetToken(pr, *tok.SecretKey); err != nil {
			return err
		}
	}
	return nil
}

// query runs an instant query and returns each result with its current
// value as the only point.
func (p *promClient) query(ctx context.Context, query string) ([]promSeries, error) {
	var data struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		} `json:"result"`
	}
	if err := p.get(ctx, "/api/v1/query", url.Values{"query": {query}}, &data); err != nil {
		return nil, err
	}
	out := make([]promSeries, len(data.Result))
	for i, r := range data.Result {
		out[i] = promSeries{labels: r.Metric}
		ts, ok1 := r.Value[0].(float64)
		vs, ok2 := r.Value[1].(string)
		if f, err := strconv.ParseFloat(vs, 64); ok1 && ok2 && err == nil {
			out[i].points = []Point{{T: time.Unix(int64(ts), 0), V: f}}
		}
	}
	return out, nil
}

// Sample is one series of an instant query.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// Query runs an instant PromQL query in one Cockpit scope. Used by
// `scwpod query` for debugging.
func (s *Service) Query(ctx context.Context, pr ProjectRegion, token, promql string) ([]Sample, error) {
	prom, err := s.prometheus(ctx, pr, token)
	if err != nil {
		return nil, err
	}
	series, err := prom.query(ctx, promql)
	if err != nil {
		return nil, err
	}
	out := make([]Sample, len(series))
	for i, sr := range series {
		out[i] = Sample{Labels: sr.labels}
		if len(sr.points) > 0 {
			out[i].Value = sr.points[0].V
		}
	}
	return out, nil
}

// DiscoverMetrics returns what one Cockpit scope provides and the queries
// scwpod would run there. Used by `scwpod metrics`.
func (s *Service) DiscoverMetrics(ctx context.Context, pr ProjectRegion, token string) (*SourceInfo, map[string][]Query, error) {
	prom, err := s.prometheus(ctx, pr, token)
	if err != nil {
		return nil, nil, err
	}
	info, err := s.sourceInfo(ctx, prom, time.Now())
	if err != nil {
		return nil, nil, err
	}
	return info, PlannedQueries(info), nil
}

// SampleLabels returns the labels of one series of each of the first
// container and database metrics in a scope, to check which label carries
// the resource ID. Used by `scwpod metrics`.
func (s *Service) SampleLabels(ctx context.Context, pr ProjectRegion, token string, info *SourceInfo) map[string]map[string]string {
	prom, err := s.prometheus(ctx, pr, token)
	if err != nil {
		return nil
	}
	out := map[string]map[string]string{}
	for _, prefix := range []string{ContainerMetricPrefix, databaseMetricRoot, InstanceMetricPrefix} {
		names := info.Names()
		if cpu := info.instanceCPU(); prefix == InstanceMetricPrefix && cpu != "" {
			names = []string{cpu} // its labels decide how CPU is queried
		}
		for _, n := range names {
			if !strings.HasPrefix(n, prefix) {
				continue
			}
			if series, err := prom.query(ctx, "topk(1, "+n+")"); err == nil && len(series) > 0 {
				out[n] = series[0].labels
			}
			break
		}
	}
	return out
}
