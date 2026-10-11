# scwpod

A terminal dashboard for your [Scaleway](https://www.scaleway.com) organization:
serverless containers, managed databases, IAM users and spending, across all
projects and regions. Runs on Linux and in Termux on Android.

> Unofficial project, not affiliated with or endorsed by Scaleway.

## Status

Early scaffold. Working today:

| Tab | Data |
|---|---|
| Overview | Live dashboard (auto-refresh on by default): container and database health, then one table per resource type with current values (last-hour charts in the detail view). Containers: CPU, memory, instances, requests/s, 5xx error rate, p95 latency. Databases: CPU, memory, disk, connections (and % of max), replication lag, WAL, transactions/queries per second, cache hit ratio |
| Projects | Per project: containers, containers not ready, databases, month-to-date spend |
| Containers | Serverless Containers in all regions: status, scaling, resources, endpoint |
| Databases | Managed Databases (RDB): engine, node type, status, HA, disk, endpoint |
| Users | IAM users: type, status, MFA, last login |
| Billing | This month: consumption, discounts, tax, total and month-end forecast vs last month. Invoice history (12 months) with totals, monthly average and year to date. Spend by category. Detailed consumption lines below |

Switch between all projects and a single project with `p` / `P`.

## Install

Download binary from the github release; or:

```sh
go install github.com/pvanek/scwpod/cmd/scwpod@latest
```

In Termux: `pkg install golang`, then the same command.

## Credentials

scwpod reads the same configuration as the [`scw` CLI](https://github.com/scaleway/scaleway-cli):
`~/.config/scw/config.yaml` and the `SCW_*` environment variables (environment wins).
Pick a profile with `scwpod --profile NAME`.

Required: `access_key`, `secret_key`, `default_organization_id`.

Recommended: create a dedicated IAM application for scwpod with an API key, and
give it a policy scoped to the organization that grants only **read-only**
permission sets for Serverless Containers, Relational Databases, IAM, Billing
and Observability. scwpod never modifies anything. A tab whose permission is
missing shows the error and the rest keep working.

## Metrics

Container and database metrics come from
[Cockpit](https://www.scaleway.com/en/docs/cockpit/), which needs a Cockpit
token per project and region. When one is missing, the Overview tab says so;
press `T` to create read-only metrics tokens named `scwpod-read-metrics`.
Creating them needs permission to manage Cockpit tokens. The secrets are
stored in `~/.config/scwpod/cockpit-tokens.json` (mode 0600). You can also put
existing tokens there yourself:

```json
{ "<project-id>/fr-par": "<cockpit token secret>" }
```

Database CPU, memory and disk are computed from the documented
`rdb_instance_<engine>_node_*` metrics, connections from
`pg_stat_database_numbackends` (PostgreSQL) or
`mysql_global_status_threads_connected` (MySQL). Connection usage,
replication lag, WAL, transactions and cache hit ratio are shown when Cockpit
provides the underlying metrics. Container metrics are picked from the
`serverless_container_*` names Cockpit reports; the error rate needs a status
code label on the request counter and the latency a duration histogram.

Containers and databases link to their Cockpit Grafana dashboards: in the
Overview tables the name opens the metrics dashboard and the Logs column the
logs; details have both too. The links are clickable in terminals that
support OSC 8 hyperlinks (in Konsole enable *Edit Current Profile → Mouse →
Allow escape sequences for links*); `m` and `L` open them with
`termux-open-url`, `xdg-open` or `open`. Container dashboards are filtered by Cockpit's name for
the container, learned from the `serverless_container_info` metric, so they
need a Cockpit token; database logs open in Grafana Explore.

Containers show whether they are actually running (`running ×N`) or scaled
to zero (`idle`). This comes from the `serverless_container_instance_info`
series in Cockpit; scwpod never calls the containers themselves, so it does
not wake them up. Metric columns no resource reports (e.g. latency when there
is no histogram) are hidden.

On narrow screens the Overview drops the last metric columns; the detail
view (`enter`) always lists every metric. To see the available metrics, the
labels of a sample series and the exact queries scwpod runs:

```sh
scwpod metrics
```

To run your own read-only PromQL query in every project and region:

```sh
scwpod query 'avg by (resource_name) (serverless_container_cpu_usage_ratio)'
```

## Keys

| Key | Action |
|---|---|
| `←` `→` / `tab` / `0`–`5` | switch tab |
| `p` / `P` | next / previous project |
| `↑` `↓` | move in table (crosses into the next table on the Overview) / scroll detail |
| `enter` | open detail of the selected row |
| `esc` / `←` | back from detail |
| `r` | refresh everything |
| `a` | toggle auto-refresh of the current tab (Overview, Projects, Containers, Databases: every 1 min; Users, Billing: every 5 min). On by default only for Overview |
| `T` | create missing Cockpit tokens (Overview) |
| `m` / `L` | open the selected container's or database's Cockpit Grafana metrics dashboard / logs in the browser (Overview, Containers, Databases, details) |
| `q` | quit |

## Architecture

```
cmd/scwpod          entry point, flags
internal/config     scw profile + env loading
internal/core       UI-agnostic service over scaleway-sdk-go, plain domain types
internal/tui        Bubble Tea front end
internal/platform   OS workarounds (DNS on Android)
```

`core` never imports UI code and does not expose SDK types, so it can later be
wrapped by a `gomobile` facade for Android and iOS apps. All data is fetched
organization-wide once and filtered by project on the client.

## Roadmap

- [ ] Object Storage buckets and sizes
- [ ] IAM applications and API keys
- [ ] Budgets and credits on the Billing tab
- [ ] Release binaries (Linux amd64/arm64, Android arm64), termux-packages
- [ ] Mobile apps over a gomobile facade of `core`

## Development

```sh
make            # build for this machine into bin/
make test       # go vet + tests
make help       # list all targets
```

Cross-compiled, static binaries go to `dist/`:

| Target | Output |
|---|---|
| `make linux` | `scwpod-linux-amd64`, `scwpod-linux-arm64` |
| `make termux` | `scwpod-termux-arm64` (Android) |
| `make windows` | `scwpod-windows-amd64.exe`, `scwpod-windows-arm64.exe` |
| `make osx` | `scwpod-macos-amd64`, `scwpod-macos-arm64` |
| `make dist` | all of the above plus `SHA256SUMS` |

The version comes from `git describe`, so tag releases (`git tag v0.1.0`).

### Releasing

Pushing a version tag runs `.github/workflows/release.yml`: tests, `make dist`
and a GitHub Release with every binary and `SHA256SUMS` attached.

```sh
git tag v0.1.0
git push origin v0.1.0
```

Tags with a suffix (`v0.2.0-rc1`) are published as pre-releases.

On GitLab, `.gitlab-ci.yml` does the same: every pipeline tests and keeps the
cross-compiled binaries as job artifacts for a week, and a `v*` tag uploads
them to the project's Generic Package Registry and creates a GitLab Release
linking to them. The release job needs GitLab 18 or newer; on older
instances use the `registry.gitlab.com/gitlab-org/release-cli:latest` image.

From a GitHub release in Termux:

```sh
curl -LO https://github.com/pvanek/scwpod/releases/latest/download/scwpod-termux-arm64
chmod +x scwpod-termux-arm64 && mv scwpod-termux-arm64 $PREFIX/bin/scwpod
```

## License

MIT
