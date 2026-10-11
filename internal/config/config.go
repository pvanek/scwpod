// Package config builds a Scaleway API client from the same sources the
// official `scw` CLI uses: ~/.config/scw/config.yaml profiles and SCW_*
// environment variables (environment wins).
package config

import (
	"errors"
	"fmt"

	"github.com/scaleway/scaleway-sdk-go/scw"
)

// NewClient returns a client for the given profile ("" = active profile).
func NewClient(profile, userAgent string) (*scw.Client, error) {
	p, err := loadProfile(profile)
	if err != nil {
		return nil, err
	}

	client, err := scw.NewClient(
		scw.WithProfile(p),
		scw.WithEnv(),
		scw.WithUserAgent(userAgent),
	)
	if err != nil {
		return nil, fmt.Errorf("creating Scaleway client: %w", err)
	}
	if _, ok := client.GetDefaultOrganizationID(); !ok {
		return nil, errors.New("no organization ID configured: set default_organization_id " +
			"in ~/.config/scw/config.yaml or SCW_DEFAULT_ORGANIZATION_ID")
	}
	return client, nil
}

func loadProfile(name string) (*scw.Profile, error) {
	cfg, err := scw.LoadConfig()
	if err != nil {
		var notFound *scw.ConfigFileNotFoundError
		if errors.As(err, &notFound) && name == "" {
			// No config file: rely on environment variables only.
			return &scw.Profile{}, nil
		}
		return nil, fmt.Errorf("loading scw config: %w", err)
	}
	if name != "" {
		return cfg.GetProfile(name)
	}
	return cfg.GetActiveProfile()
}
