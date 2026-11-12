package core

import (
	"context"
	"fmt"
	"sort"
	"strings"

	instance "github.com/scaleway/scaleway-sdk-go/api/instance/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

// Instances lists the virtual machines of the organization in all zones.
func (s *Service) Instances(ctx context.Context) ([]Instance, error) {
	out, err := perZone(s.instance.Zones(), func(zone scw.Zone) ([]Instance, error) {
		resp, err := s.instance.ListServers(&instance.ListServersRequest{
			Zone:         zone,
			Organization: &s.orgID,
		}, scw.WithContext(ctx), scw.WithAllPages())
		if err != nil {
			return nil, fmt.Errorf("listing instances in %s: %w", zone, err)
		}
		if len(resp.Servers) == 0 {
			return nil, nil
		}
		// Sizes of the commercial types, to show usage as a percentage.
		// Best effort: without them metrics stay absolute.
		types := map[string]*instance.ServerType{}
		if tr, err := s.instance.ListServersTypes(&instance.ListServersTypesRequest{Zone: zone},
			scw.WithContext(ctx), scw.WithAllPages()); err == nil {
			types = tr.Servers
		}
		out := make([]Instance, 0, len(resp.Servers))
		for _, srv := range resp.Servers {
			in := toInstance(srv)
			if t := types[srv.CommercialType]; t != nil {
				in.VCPUs, in.RAMBytes = t.Ncpus, t.RAM
			}
			out = append(out, in)
		}
		return out, nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Zone < out[j].Zone
	})
	return out, err
}

func toInstance(srv *instance.Server) Instance {
	in := Instance{
		ID:           srv.ID,
		Name:         srv.Name,
		ProjectID:    srv.Project,
		Zone:         string(srv.Zone),
		Status:       srv.State.String(),
		StateDetail:  srv.StateDetail,
		Type:         srv.CommercialType,
		Arch:         srv.Arch.String(),
		Hostname:     srv.Hostname,
		PrivateNICs:  len(srv.PrivateNics),
		Protected:    srv.Protected,
		EndOfService: srv.EndOfService,
		Tags:         srv.Tags,
		CreatedAt:    derefTime(srv.CreationDate),
		UpdatedAt:    derefTime(srv.ModificationDate),
	}
	if srv.Image != nil {
		in.Image = srv.Image.Name
	}
	for _, ip := range srv.PublicIPs {
		if ip != nil && ip.Address != nil {
			in.PublicIPs = append(in.PublicIPs, ip.Address.String())
		}
	}
	if len(in.PublicIPs) == 0 && srv.PublicIP != nil && srv.PublicIP.Address != nil {
		in.PublicIPs = []string{srv.PublicIP.Address.String()}
	}
	if srv.PrivateIP != nil {
		in.PrivateIP = *srv.PrivateIP
	}
	if srv.SecurityGroup != nil {
		in.SecurityGroup = srv.SecurityGroup.Name
	}
	if srv.PlacementGroup != nil {
		in.PlacementGroup = srv.PlacementGroup.Name
	}
	for _, m := range srv.Maintenances {
		if m == nil {
			continue
		}
		desc := m.Reason
		if m.StartDate != nil {
			desc = m.StartDate.Local().Format("2006-01-02 15:04") + " " + desc
		}
		in.Maintenances = append(in.Maintenances, strings.TrimSpace(desc))
	}
	in.Volumes = volumeLines(srv.Volumes)
	return in
}

// volumeLines describes the attached volumes in slot order ("0" is the
// boot volume).
func volumeLines(vols map[string]*instance.VolumeServer) []string {
	slots := make([]string, 0, len(vols))
	for k := range vols {
		slots = append(slots, k)
	}
	sort.Slice(slots, func(i, j int) bool {
		if len(slots[i]) != len(slots[j]) {
			return len(slots[i]) < len(slots[j])
		}
		return slots[i] < slots[j]
	})
	var out []string
	for _, k := range slots {
		v := vols[k]
		if v == nil {
			continue
		}
		parts := []string{}
		if v.Name != nil && *v.Name != "" {
			parts = append(parts, *v.Name)
		} else {
			parts = append(parts, v.ID)
		}
		if v.Size != nil && *v.Size > 0 {
			parts = append(parts, fmt.Sprintf("%dGB", uint64(*v.Size)/1_000_000_000))
		}
		parts = append(parts, string(v.VolumeType))
		if v.Boot {
			parts = append(parts, "(boot)")
		}
		out = append(out, strings.Join(parts, " "))
	}
	return out
}
