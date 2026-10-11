package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	cockpit "github.com/scaleway/scaleway-sdk-go/api/cockpit/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

// Every project has its own Cockpit Grafana with Scaleway's product
// dashboards. These links open them filtered to one resource.

// Links are Grafana URLs for one resource; empty when unknown.
type Links struct {
	Metrics string
	Logs    string
}

// Product dashboards (uid/slug) as listed by the Cockpit API, October 2026.
const (
	dashContainers     = "/d/scw-serverless-containers-overview/serverless-containers-overview"
	dashContainerLogs  = "/d/scw-serverless-containers-logs/serverless-containers-logs"
	dashPostgreSQL     = "/d/scw-rdb-postgresql-overview/rdb-postgresql-overview"
	dashMySQL          = "/d/scw-rdb-mysql-overview/rdb-mysql-overview"
	logsDataSourceName = "Scaleway Logs"
	linkTimeRange      = "now-1h"
)

// ContainerLinks builds links for a container. resourceName is Cockpit's
// name for it (the resource_name metric label); without it the dashboards
// open unfiltered.
func ContainerLinks(grafanaURL, region, resourceName string) Links {
	if grafanaURL == "" {
		return Links{}
	}
	q := url.Values{"var-region": {region}, "from": {linkTimeRange}, "to": {"now"}}
	if resourceName != "" {
		q.Set("var-container_name", resourceName)
	}
	return Links{
		Metrics: grafanaURL + dashContainers + "?" + q.Encode(),
		Logs:    grafanaURL + dashContainerLogs + "?" + q.Encode(),
	}
}

// DatabaseLinks builds links for a database. Its dashboard is filtered by
// the database name; logs open in Grafana Explore on the Scaleway logs
// data source.
func DatabaseLinks(grafanaURL, region, engine, name string) Links {
	if grafanaURL == "" {
		return Links{}
	}
	dash := dashPostgreSQL
	if strings.HasPrefix(strings.ToLower(engine), "mysql") {
		dash = dashMySQL
	}
	q := url.Values{"var-region": {region}, "var-resource_name": {name}, "from": {linkTimeRange}, "to": {"now"}}

	explore, _ := json.Marshal(map[string]any{
		"datasource": logsDataSourceName,
		"queries":    []map[string]string{{"refId": "A", "expr": fmt.Sprintf(`{resource_name=%q}`, name)}},
		"range":      map[string]string{"from": linkTimeRange, "to": "now"},
	})
	return Links{
		Metrics: grafanaURL + dash + "?" + q.Encode(),
		Logs:    grafanaURL + "/explore?" + url.Values{"left": {string(explore)}}.Encode(),
	}
}

// GrafanaURLs returns the Cockpit Grafana URL of each project, cached for
// the session. Failures are returned as an error alongside what succeeded.
func (s *Service) GrafanaURLs(ctx context.Context, projectIDs []string) (map[string]string, error) {
	out := map[string]string{}
	var errs []error
	for _, id := range projectIDs {
		s.infoMu.Lock()
		cached, ok := s.grafana[id]
		s.infoMu.Unlock()
		if ok {
			out[id] = cached
			continue
		}
		g, err := s.cockpitGlobal.GetGrafana(&cockpit.GlobalAPIGetGrafanaRequest{ProjectID: id}, scw.WithContext(ctx))
		if err != nil {
			errs = append(errs, fmt.Errorf("grafana URL of project %s: %w", id, err))
			continue
		}
		url := strings.TrimRight(g.GrafanaURL, "/")
		s.infoMu.Lock()
		s.grafana[id] = url
		s.infoMu.Unlock()
		out[id] = url
	}
	return out, joinErrs(errs)
}
