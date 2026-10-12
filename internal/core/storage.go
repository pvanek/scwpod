package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	block "github.com/scaleway/scaleway-sdk-go/api/block/v1"
	file "github.com/scaleway/scaleway-sdk-go/api/file/v1alpha1"
	instance "github.com/scaleway/scaleway-sdk-go/api/instance/v1"
	registry "github.com/scaleway/scaleway-sdk-go/api/registry/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

// Storage returns container registry namespaces, Block Storage volumes,
// Instance local volumes and File Storage filesystems of the organization.
// Object Storage buckets come from Buckets, which needs the project list.
// Products that fail (e.g. missing permission) are reported in the error
// while the others are still returned.
func (s *Service) Storage(ctx context.Context) ([]StorageItem, error) {
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		out  []StorageItem
		errs []error
	)
	run := func(fn func(context.Context) ([]StorageItem, error)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, err := fn(ctx)
			mu.Lock()
			defer mu.Unlock()
			out = append(out, items...)
			if err != nil {
				errs = append(errs, err)
			}
		}()
	}
	run(s.registryNamespaces)
	run(s.volumes)
	run(s.fileSystems)
	wg.Wait()
	sortStorage(out)
	return out, errors.Join(errs...)
}

func sortStorage(items []StorageItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].Name < items[j].Name
	})
}

func (s *Service) registryNamespaces(ctx context.Context) ([]StorageItem, error) {
	return perRegion(s.registry.Regions(), func(region scw.Region) ([]StorageItem, error) {
		resp, err := s.registry.ListNamespaces(&registry.ListNamespacesRequest{
			Region:         region,
			OrganizationID: &s.orgID,
		}, scw.WithContext(ctx), scw.WithAllPages())
		if err != nil {
			return nil, fmt.Errorf("listing registry namespaces in %s: %w", region, err)
		}
		var out []StorageItem
		for _, ns := range resp.Namespaces {
			images := int(ns.ImageCount)
			out = append(out, StorageItem{
				Kind:          StorageRegistry,
				ID:            ns.ID,
				Name:          ns.Name,
				ProjectID:     ns.ProjectID,
				Location:      string(ns.Region),
				Status:        ns.Status.String(),
				StatusMessage: ns.StatusMessage,
				SizeBytes:     uint64(ns.Size),
				Endpoint:      ns.Endpoint,
				Public:        ns.IsPublic,
				Images:        &images,
				Description:   ns.Description,
				CreatedAt:     derefTime(ns.CreatedAt),
				UpdatedAt:     derefTime(ns.UpdatedAt),
			})
		}
		return out, nil
	})
}

// volumes lists Block Storage volumes and Instance local volumes per zone,
// naming the Instances they are attached to.
func (s *Service) volumes(ctx context.Context) ([]StorageItem, error) {
	return perZone(s.instance.Zones(), func(zone scw.Zone) ([]StorageItem, error) {
		servers, err := s.instance.ListServers(&instance.ListServersRequest{
			Zone:         zone,
			Organization: &s.orgID,
		}, scw.WithContext(ctx), scw.WithAllPages())
		if err != nil {
			return nil, fmt.Errorf("listing instances in %s: %w", zone, err)
		}
		serverName := map[string]string{}
		for _, srv := range servers.Servers {
			serverName[srv.ID] = srv.Name
		}

		lssd := instance.VolumeVolumeTypeLSSD
		local, err := s.instance.ListVolumes(&instance.ListVolumesRequest{
			Zone:         zone,
			VolumeType:   &lssd,
			Organization: &s.orgID,
		}, scw.WithContext(ctx), scw.WithAllPages())
		if err != nil {
			return nil, fmt.Errorf("listing local volumes in %s: %w", zone, err)
		}
		var out []StorageItem
		for _, v := range local.Volumes {
			item := StorageItem{
				Kind:      StorageLocal,
				ID:        v.ID,
				Name:      v.Name,
				ProjectID: v.Project,
				Location:  string(v.Zone),
				Status:    v.State.String(),
				SizeBytes: uint64(v.Size),
				Class:     string(v.VolumeType),
				Tags:      v.Tags,
				CreatedAt: derefTime(v.CreationDate),
				UpdatedAt: derefTime(v.ModificationDate),
			}
			if v.Server != nil {
				item.AttachedTo = []string{v.Server.Name}
			}
			out = append(out, item)
		}

		if !hasZone(s.block.Zones(), zone) {
			return out, nil
		}
		blocks, err := s.block.ListVolumes(&block.ListVolumesRequest{
			Zone:           zone,
			OrganizationID: &s.orgID,
		}, scw.WithContext(ctx), scw.WithAllPages())
		if err != nil {
			return out, fmt.Errorf("listing block volumes in %s: %w", zone, err)
		}
		for _, v := range blocks.Volumes {
			item := StorageItem{
				Kind:      StorageBlock,
				ID:        v.ID,
				Name:      v.Name,
				ProjectID: v.ProjectID,
				Location:  string(v.Zone),
				Status:    v.Status.String(),
				SizeBytes: uint64(v.Size),
				Class:     v.Type,
				Tags:      v.Tags,
				CreatedAt: derefTime(v.CreatedAt),
				UpdatedAt: derefTime(v.UpdatedAt),
			}
			if v.Specs != nil && v.Specs.PerfIops != nil {
				item.IOPS = *v.Specs.PerfIops
			}
			for _, ref := range v.References {
				name := serverName[ref.ProductResourceID]
				if name == "" {
					name = ref.ProductResourceType + " " + ref.ProductResourceID
				}
				item.AttachedTo = append(item.AttachedTo, name)
			}
			out = append(out, item)
		}
		return out, nil
	})
}

func (s *Service) fileSystems(ctx context.Context) ([]StorageItem, error) {
	return perRegion(s.file.Regions(), func(region scw.Region) ([]StorageItem, error) {
		resp, err := s.file.ListFileSystems(&file.ListFileSystemsRequest{
			Region:         region,
			OrganizationID: &s.orgID,
		}, scw.WithContext(ctx), scw.WithAllPages())
		if err != nil {
			return nil, fmt.Errorf("listing file systems in %s: %w", region, err)
		}
		var out []StorageItem
		for _, fs := range resp.Filesystems {
			attachments := int(fs.NumberOfAttachments)
			out = append(out, StorageItem{
				Kind:        StorageFile,
				ID:          fs.ID,
				Name:        fs.Name,
				ProjectID:   fs.ProjectID,
				Location:    string(fs.Region),
				Status:      fs.Status.String(),
				SizeBytes:   uint64(fs.Size),
				Attachments: &attachments,
				Tags:        fs.Tags,
				CreatedAt:   derefTime(fs.CreatedAt),
				UpdatedAt:   derefTime(fs.UpdatedAt),
			})
		}
		return out, nil
	})
}

func hasZone(zones []scw.Zone, z scw.Zone) bool {
	for _, zone := range zones {
		if zone == z {
			return true
		}
	}
	return false
}

// perZone is perRegion for zonal products.
func perZone[T any](zones []scw.Zone, fn func(scw.Zone) ([]T, error)) ([]T, error) {
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		out  []T
		errs []error
	)
	for _, z := range zones {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, err := fn(z)
			mu.Lock()
			defer mu.Unlock()
			out = append(out, items...)
			if err != nil {
				errs = append(errs, err)
			}
		}()
	}
	wg.Wait()
	return out, errors.Join(errs...)
}
