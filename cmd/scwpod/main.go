// Command scwpod is a terminal dashboard for a Scaleway organization.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/scaleway/scaleway-sdk-go/scw"

	"github.com/pvanek/scwpod/internal/config"
	"github.com/pvanek/scwpod/internal/core"
	"github.com/pvanek/scwpod/internal/platform"
	"github.com/pvanek/scwpod/internal/tui"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `Usage: scwpod [flags] [command]

Commands:
  (none)    start the dashboard
  metrics   list the container, database and Instance metrics Cockpit provides
  query Q   run the PromQL query Q in every project and region (read-only)

Flags:
`

func main() {
	profile := flag.String("profile", "", "scw config profile to use (default: active profile)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), usage)
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVersion {
		fmt.Println("scwpod", version)
		return
	}
	if err := run(*profile, flag.Arg(0)); err != nil {
		fmt.Fprintln(os.Stderr, "scwpod:", err)
		os.Exit(1)
	}
}

func run(profile, command string) error {
	platform.FixDNS()

	client, err := config.NewClient(profile, "scwpod/"+version)
	if err != nil {
		return err
	}
	svc, err := core.NewService(client)
	if err != nil {
		return err
	}
	tokenPath, err := config.DefaultTokenPath()
	if err != nil {
		return err
	}
	tokens, err := config.LoadTokenFile(tokenPath)
	if err != nil {
		return err
	}
	b := backend{Service: svc, tokens: tokens}

	switch command {
	case "":
		_, err = tea.NewProgram(tui.New(b)).Run()
		return err
	case "metrics":
		return printMetrics(b)
	case "query":
		if flag.NArg() < 2 {
			return fmt.Errorf("usage: scwpod query '<promql>'")
		}
		return printQuery(b, flag.Arg(1))
	default:
		flag.Usage()
		return fmt.Errorf("unknown command %q", command)
	}
}

// backend binds the Cockpit token store to the service for the UI.
type backend struct {
	*core.Service
	tokens core.CockpitTokens
}

func (b backend) Metrics(ctx context.Context, cs []core.Container, dbs []core.Database, ins []core.Instance, now time.Time) (core.Metrics, error) {
	return b.Service.Metrics(ctx, cs, dbs, ins, b.tokens, now)
}

func (b backend) CreateCockpitTokens(ctx context.Context, scopes []core.ProjectRegion) error {
	return b.Service.CreateCockpitTokens(ctx, scopes, b.tokens)
}

// printQuery runs promql in every scope that has a Cockpit token and prints
// each resulting series with its labels.
func printQuery(b backend, promql string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	scopes, err := resourceScopes(ctx, b)
	if err != nil {
		return err
	}
	for _, pr := range scopes {
		fmt.Printf("project %s, region %s\n", pr.ProjectID, pr.Region)
		token := b.tokens.Token(pr)
		if token == "" {
			fmt.Println("  no Cockpit token")
			continue
		}
		samples, err := b.Query(ctx, pr, token, promql)
		if err != nil {
			fmt.Println("  error:", err)
			continue
		}
		for _, s := range samples {
			keys := make([]string, 0, len(s.Labels))
			for k := range s.Labels {
				if k != "project_id" && k != "project_name" && k != "region" {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			parts := make([]string, len(keys))
			for i, k := range keys {
				parts[i] = fmt.Sprintf("%s=%q", k, s.Labels[k])
			}
			fmt.Printf("  %g  {%s}\n", s.Value, strings.Join(parts, ", "))
		}
	}
	return nil
}

// resourceScopes returns the project/region pairs that have containers or
// databases.
func resourceScopes(ctx context.Context, b backend) ([]core.ProjectRegion, error) {
	containers, err := b.Containers(ctx)
	if err != nil {
		return nil, err
	}
	dbs, err := b.Databases(ctx)
	if err != nil {
		return nil, err
	}
	var scopes []core.ProjectRegion
	seen := map[core.ProjectRegion]bool{}
	add := func(pr core.ProjectRegion) {
		if !seen[pr] {
			seen[pr] = true
			scopes = append(scopes, pr)
		}
	}
	for _, c := range containers {
		add(core.ProjectRegion{ProjectID: c.ProjectID, Region: c.Region})
	}
	for _, d := range dbs {
		add(core.ProjectRegion{ProjectID: d.ProjectID, Region: d.Region})
	}
	instances, err := b.Instances(ctx)
	if err != nil {
		return nil, err
	}
	for _, in := range instances {
		if region, err := scw.Zone(in.Zone).Region(); err == nil {
			add(core.ProjectRegion{ProjectID: in.ProjectID, Region: string(region)})
		}
	}
	return scopes, nil
}

// printMetrics shows, per project and region with containers, databases or Instances,
// which Scaleway metrics Cockpit has, which container metrics the dashboard
// picks, and the labels of a sample series.
func printMetrics(b backend) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	scopes, err := resourceScopes(ctx, b)
	if err != nil {
		return err
	}
	if len(scopes) == 0 {
		fmt.Println("no containers, databases or Instances found")
		return nil
	}

	for _, pr := range scopes {
		fmt.Printf("project %s, region %s\n", pr.ProjectID, pr.Region)
		token := b.tokens.Token(pr)
		if token == "" {
			fmt.Println("  no Cockpit token: press T on the Overview tab to create one")
			continue
		}
		info, planned, err := b.DiscoverMetrics(ctx, pr, token)
		if err != nil {
			fmt.Println("  error:", err)
			continue
		}
		fmt.Println("  available metrics:")
		for _, n := range info.Names() {
			fmt.Println("    ", n)
		}
		fmt.Printf("  status label on requests: %q\n", info.StatusLabel)
		for name, labels := range b.SampleLabels(ctx, pr, token, info) {
			fmt.Printf("  sample labels of %s:\n", name)
			keys := make([]string, 0, len(labels))
			for k := range labels {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Printf("    %s=%q\n", k, labels[k])
			}
		}
		for _, kind := range []string{"containers", "instances", "postgresql", "mysql"} {
			fmt.Printf("  queries for %s:\n", kind)
			for _, q := range planned[kind] {
				fmt.Printf("    %-16s %s\n", q.Kind, q.PromQL)
			}
		}
	}
	return nil
}
