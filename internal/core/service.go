package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	account "github.com/scaleway/scaleway-sdk-go/api/account/v3"
	billing "github.com/scaleway/scaleway-sdk-go/api/billing/v2beta1"
	block "github.com/scaleway/scaleway-sdk-go/api/block/v1"
	cockpit "github.com/scaleway/scaleway-sdk-go/api/cockpit/v1"
	container "github.com/scaleway/scaleway-sdk-go/api/container/v1"
	file "github.com/scaleway/scaleway-sdk-go/api/file/v1alpha1"
	iam "github.com/scaleway/scaleway-sdk-go/api/iam/v1alpha1"
	instance "github.com/scaleway/scaleway-sdk-go/api/instance/v1"
	rdb "github.com/scaleway/scaleway-sdk-go/api/rdb/v1"
	registry "github.com/scaleway/scaleway-sdk-go/api/registry/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

// Service fetches organization-wide data. Every list call covers all
// projects (and all regions where relevant); callers filter by project.
type Service struct {
	orgID     string
	projects  *account.ProjectAPI
	container *container.API
	rdb       *rdb.API
	iam       *iam.API
	billing   *billing.API
	cockpit   *cockpit.RegionalAPI
	registry  *registry.API
	block     *block.API
	instance  *instance.API
	file      *file.API

	cockpitGlobal *cockpit.GlobalAPI

	// Object Storage is reached over S3 with the API key.
	accessKey, secretKey string
	httpClient           *http.Client
	s3URL                func(scw.Region) string

	infoMu  sync.Mutex
	info    map[string]*SourceInfo // per Cockpit data source URL
	grafana map[string]string      // Grafana URL per project ID
}

func NewService(client *scw.Client) (*Service, error) {
	orgID, ok := client.GetDefaultOrganizationID()
	if !ok {
		return nil, errors.New("client has no default organization ID")
	}
	accessKey, _ := client.GetAccessKey()
	secretKey, _ := client.GetSecretKey()
	return &Service{
		orgID:     orgID,
		projects:  account.NewProjectAPI(client),
		container: container.NewAPI(client),
		rdb:       rdb.NewAPI(client),
		iam:       iam.NewAPI(client),
		billing:   billing.NewAPI(client),
		cockpit:   cockpit.NewRegionalAPI(client),
		registry:  registry.NewAPI(client),
		block:     block.NewAPI(client),
		instance:  instance.NewAPI(client),
		file:      file.NewAPI(client),
		info:      map[string]*SourceInfo{},
		grafana:   map[string]string{},

		cockpitGlobal: cockpit.NewGlobalAPI(client),

		accessKey:  accessKey,
		secretKey:  secretKey,
		httpClient: &http.Client{Timeout: 20 * time.Second},
		s3URL:      s3URL,
	}, nil
}

func (s *Service) Projects(ctx context.Context) ([]Project, error) {
	resp, err := s.projects.ListProjects(&account.ProjectAPIListProjectsRequest{
		OrganizationID: s.orgID,
		OrderBy:        account.ListProjectsRequestOrderByNameAsc,
	}, scw.WithContext(ctx), scw.WithAllPages())
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	out := make([]Project, 0, len(resp.Projects))
	for _, p := range resp.Projects {
		out = append(out, Project{
			ID:          p.ID,
			Name:        p.Name,
			Description: p.Description,
			CreatedAt:   derefTime(p.CreatedAt),
		})
	}
	return out, nil
}

func (s *Service) Containers(ctx context.Context) ([]Container, error) {
	out, err := perRegion(s.container.Regions(), func(region scw.Region) ([]Container, error) {
		nsResp, err := s.container.ListNamespaces(&container.ListNamespacesRequest{
			Region:         region,
			OrganizationID: &s.orgID,
		}, scw.WithContext(ctx), scw.WithAllPages())
		if err != nil {
			return nil, fmt.Errorf("listing container namespaces in %s: %w", region, err)
		}
		if len(nsResp.Namespaces) == 0 {
			return nil, nil
		}
		namespaces := make(map[string]*container.Namespace, len(nsResp.Namespaces))
		for _, ns := range nsResp.Namespaces {
			namespaces[ns.ID] = ns
		}

		cResp, err := s.container.ListContainers(&container.ListContainersRequest{
			Region:         region,
			OrganizationID: &s.orgID,
		}, scw.WithContext(ctx), scw.WithAllPages())
		if err != nil {
			return nil, fmt.Errorf("listing containers in %s: %w", region, err)
		}
		var out []Container
		for _, c := range cResp.Containers {
			item := Container{
				ID:        c.ID,
				Name:      c.Name,
				Region:    string(c.Region),
				Status:    c.Status.String(),
				Image:     c.Image,
				Endpoint:  c.PublicEndpoint,
				MinScale:  c.MinScale,
				MaxScale:  c.MaxScale,
				MemoryMB:  uint64(c.MemoryLimitBytes) / 1_000_000,
				MilliVCPU: c.MvcpuLimit,
			}
			if ns := namespaces[c.NamespaceID]; ns != nil {
				item.ProjectID = ns.ProjectID
				item.Namespace = ns.Name
			}
			if c.ErrorMessage != nil {
				item.ErrorReason = *c.ErrorMessage
			}
			item.Description = c.Description
			item.Privacy = c.Privacy.String()
			item.Protocol = c.Protocol.String()
			item.Port = c.Port
			if d := c.Timeout.ToTimeDuration(); d != nil {
				item.Timeout = *d
			}
			if c.PrivateEndpoint != nil {
				item.PrivateEndpoint = *c.PrivateEndpoint
			}
			item.Tags = c.Tags
			item.Env = c.EnvironmentVariables
			item.CreatedAt = derefTime(c.CreatedAt)
			item.UpdatedAt = derefTime(c.UpdatedAt)
			out = append(out, item)
		}
		return out, nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, err
}

func (s *Service) Databases(ctx context.Context) ([]Database, error) {
	out, err := perRegion(s.rdb.Regions(), func(region scw.Region) ([]Database, error) {
		resp, err := s.rdb.ListInstances(&rdb.ListInstancesRequest{
			Region:         region,
			OrganizationID: &s.orgID,
		}, scw.WithContext(ctx), scw.WithAllPages())
		if err != nil {
			return nil, fmt.Errorf("listing databases in %s: %w", region, err)
		}
		var out []Database
		for _, i := range resp.Instances {
			db := Database{
				ID:        i.ID,
				Name:      i.Name,
				ProjectID: i.ProjectID,
				Region:    string(i.Region),
				Engine:    i.Engine,
				NodeType:  i.NodeType,
				Status:    i.Status.String(),
				HA:        i.IsHaCluster,
				Endpoint:  endpointString(i.Endpoints),
			}
			if i.Volume != nil {
				db.VolumeGB = uint64(i.Volume.Size) / 1_000_000_000
				db.VolumeType = i.Volume.Type.String()
			}
			for _, ep := range i.Endpoints {
				if s := endpointString([]*rdb.Endpoint{ep}); s != "" {
					db.Endpoints = append(db.Endpoints, s)
				}
			}
			db.Encrypted = i.Encryption != nil && i.Encryption.Enabled
			db.ReadReplicas = len(i.ReadReplicas)
			if b := i.BackupSchedule; b != nil {
				db.BackupEnabled = !b.Disabled
				db.BackupEveryH = b.Frequency
				db.BackupKeepDays = b.Retention
				db.NextBackupAt = derefTime(b.NextRunAt)
			}
			for _, mt := range i.Maintenances {
				if mt.ClosedAt != nil {
					continue
				}
				desc := mt.Reason
				if mt.StartsAt != nil {
					desc = mt.StartsAt.Local().Format("2006-01-02 15:04") + " " + desc
				}
				db.Maintenances = append(db.Maintenances, strings.TrimSpace(desc+" ("+mt.Status.String()+")"))
			}
			for _, u := range i.UpgradableVersion {
				db.Upgrades = append(db.Upgrades, u.Name)
			}
			db.Tags = i.Tags
			db.CreatedAt = derefTime(i.CreatedAt)
			out = append(out, db)
		}
		return out, nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, err
}

func (s *Service) Users(ctx context.Context) ([]User, error) {
	resp, err := s.iam.ListUsers(&iam.ListUsersRequest{
		OrganizationID: &s.orgID,
		OrderBy:        iam.ListUsersRequestOrderByEmailAsc,
	}, scw.WithContext(ctx), scw.WithAllPages())
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	out := make([]User, 0, len(resp.Users))
	for _, u := range resp.Users {
		user := User{
			ID:          u.ID,
			Email:       u.Email,
			Username:    u.Username,
			Type:        u.Type.String(),
			MFA:         u.Mfa,
			LastLoginAt: derefTime(u.LastLoginAt),
			FirstName:   u.FirstName,
			LastName:    u.LastName,
			Locked:      u.Locked,
			Tags:        u.Tags,
			CreatedAt:   derefTime(u.CreatedAt),
		}
		if u.Status != nil {
			user.Status = u.Status.String()
		}
		out = append(out, user)
	}
	return out, nil
}

// CurrentPeriod returns the billing period string for t.
func CurrentPeriod(t time.Time) string { return t.Format("2006-01") }

// perRegion runs fn for every region concurrently and concatenates results.
// Results from regions that succeeded are returned even if others failed.
func perRegion[T any](regions []scw.Region, fn func(scw.Region) ([]T, error)) ([]T, error) {
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		out  []T
		errs []error
	)
	for _, r := range regions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, err := fn(r)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			out = append(out, items...)
		}()
	}
	wg.Wait()
	return out, errors.Join(errs...)
}

func endpointString(eps []*rdb.Endpoint) string {
	for _, ep := range eps {
		if ep == nil {
			continue
		}
		host := ""
		switch {
		case ep.Hostname != nil && *ep.Hostname != "":
			host = *ep.Hostname
		case ep.IP != nil:
			host = ep.IP.String()
		default:
			continue
		}
		return net.JoinHostPort(host, strconv.Itoa(int(ep.Port)))
	}
	return ""
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
