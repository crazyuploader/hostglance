# HostGlance

Lightweight system and storage monitoring for your hosts, with a **HostGlance** dashboard. Configure hostnames or IP addresses; the app discovers [node_exporter](https://github.com/prometheus/node_exporter), [pdf/zfs_exporter](https://github.com/pdf/zfs_exporter), and [smartctl_exporter](https://github.com/prometheus-community/smartctl_exporter), then shows the available metrics.

ZFS is optional. Hosts with only system metrics or disk health work independently.

## Run

Running from source requires Go 1.26.2 or later.

Create `config.yaml` with the hosts you want to monitor:

```yaml
hosts:
  - nas.home
  - server02.home
  - 192.168.1.20
```

```bash
go run . serve --config config.yaml
```

To validate the config and probe every host once without starting the server, run `go run . check --config config.yaml`. It prints which exporters each host has and exits non-zero if an exporter with `mode: enabled` is unreachable.

Open `http://localhost:8054`. The System page lists every configured host. Storage appears when ZFS or SMART metrics are available, or when either exporter is explicitly required. An empty host shows **No exporters detected**.

For more settings, copy [config.yaml.example](config.yaml.example) to `config.yaml` and edit its hosts.

### Flags

- `--config`: Config file (searches `./config.yaml`, then `~/.config/hostglance/config.yaml` by default).
- `--hosts`: Comma-separated or repeated hostnames or IP addresses, with automatic exporter discovery.
- `--endpoints`: Legacy comma-separated or repeated ZFS exporter URLs; cannot be combined with `hosts`.
- `--addr`: Address to listen on (default: `:8054`).
- `--refresh`: Discovery and metrics refresh interval in seconds (default: `300`). The config file also accepts duration strings like `"5m"`.
- `--debug`: Enable verbose debug logging.
- `--trusted-proxies`: List of trusted proxy IPs for reverse proxy header support.
- `--max-usage-percent`: Pool usage threshold for health failure (default: `0`, disabled).
- `--log-format`: Log format, either `text` or `json` (default: `text`).
- `--history-enabled`: Enable time-series history storage (default: `false`).
- `--history-path`: Path to history database file (default: `./data/history.db`).
- `--history-retention`: Retention period, e.g. `720h` for 30 days (default: `720h`).
- `--history-record-interval`: How often to record history samples, e.g. `5m` (default: same as `--refresh`).

For example, `go run . serve --hosts nas.home,server02.home` configures two hosts. `HOSTGLANCE_HOSTS=nas.home,server02.home` provides the same list through the environment. Nested settings use underscores in environment variables, for example `HOSTGLANCE_HISTORY_ENABLED=true`. Flags take precedence over environment variables, which take precedence over file settings. Unknown settings, an invalid `log_format`, and a `max_usage_percent` outside 0–100 are rejected at startup. Use only one target format, `hosts` or `endpoints`, across all configuration sources.

## Config

```yaml
addr: ":8054"
refresh: 300
cache_ttl: 30 # cache fetched metrics for 30 seconds
max_usage_percent: 90 # fail health check if any pool > 90% full
log_format: "text" # "text" or "json"
debug: false
trusted_proxies: [] # e.g., ["127.0.0.1", "100.64.0.0/10"]

history:
  enabled: false
  path: "./data/history.db"
  retention: "720h" # 30 days; supports Go duration strings
  record_interval: "5m" # sample frequency; defaults to refresh interval

hosts:
  - server02.home
  - address: nas.home
    label: node-1
    location: Singapore
    exporters:
      zfs:
        mode: enabled # required; an unavailable exporter produces an error
      node:
        mode: auto # optional; also the default when omitted
      smartctl:
        mode: disabled # do not probe or collect
  - address: "2001:db8::20"
    label: node-2
    exporters:
      node:
        url: "https://metrics.example.net/server02/node/metrics"
        # A custom URL alone keeps mode: auto.
```

Entries can be bare hostnames/IP addresses or objects. `address` is a hostname or IP address without a scheme, port, or path; use exporter `url` overrides for those. Bare IPv6 addresses are supported. `label` defaults to `address` and must be unique. Choose stable labels because health URLs and history series use them.

### Exporter discovery

Every unspecified exporter defaults to `auto`. Discovery checks known endpoints on the configured hosts and recognizes exporter-specific metric families:

| Exporter   | Default endpoint             | Data                                                  |
| ---------- | ---------------------------- | ----------------------------------------------------- |
| `node`     | `http://<host>:9100/metrics` | CPU, memory, load, network, filesystems, temperatures |
| `zfs`      | `http://<host>:9134/metrics` | ZFS pools and datasets                                |
| `smartctl` | `http://<host>:9633/metrics` | Disk health, temperature, wear                        |

This does not scan your network or install exporters. The exporters must already be running and reachable from the app. Use `url` to override a port, scheme, or path, including HTTPS or a reverse proxy.

| Mode       | Behavior                                                                                                                    |
| ---------- | --------------------------------------------------------------------------------------------------------------------------- |
| `auto`     | Collect when a recognized exporter responds. Missing or unavailable exporters are silent and their sections are hidden.     |
| `enabled`  | Require this exporter. Keep its section visible and report an error if it is unavailable or its response is not recognized. |
| `disabled` | Never request or collect this exporter's metrics.                                                                           |

Discovery runs at startup and repeats with metrics refreshes. Starting an exporter later makes its metrics appear automatically; an unavailable automatic exporter is retried without marking the host unhealthy. Other exporters continue to work when one fails. An exporter that reports an unhealthy pool still produces a health failure, even in `auto` mode.

### Migrate existing configuration

The `endpoints` format and `--endpoints` flag remain supported. A legacy entry's `url` is required ZFS; supplied `node_exporter_url` and `smartctl_url` values are optional. Omitted companion URLs do not trigger discovery probes.

Legacy combined endpoints continue to collect SMART metrics bundled into the ZFS response. When bundled SMART metrics are found, the public SMART status can be `mode: auto`, `available: true` without a separate `smartctl_url`. New `hosts` entries collect each source independently and always honor `smartctl.mode: disabled`.

For example, this existing entry:

```yaml
endpoints:
  - url: "http://nas.home:9134/metrics"
    label: node-1
    location: Singapore
    node_exporter_url: "http://nas.home:9100/metrics"
```

can become:

```yaml
hosts:
  - address: nas.home
    label: node-1
    location: Singapore
    exporters:
      zfs:
        mode: enabled
      smartctl:
        mode: disabled
```

This preserves the original requirements and skips SMART collection. Remove the `smartctl` override to discover SMART metrics too, or change ZFS to `auto` to make it optional. Retain any custom exporter URLs when migrating. If the old ZFS URL also served SMART metrics, configure that same URL under `exporters.smartctl` with `mode: auto` to keep collecting them.

Remove the old `endpoints` setting, including any `--endpoints` flag or `HOSTGLANCE_ENDPOINTS` environment variable. Mixing it with `hosts` is rejected. Keep the same labels and `history.path` to continue existing history without a database conversion. If an old entry omitted `label`, its label was its ZFS URL; copy that value explicitly to preserve its history keys.

The executable is `hostglance`, the container image is `ghcr.io/crazyuploader/hostglance`, environment variables use the `HOSTGLANCE_*` prefix, and the default user config directory is `~/.config/hostglance`.

## System and Storage

The **System** homepage (`/`, also available at `/system`) lists hosts and available system metrics:

- CPU busy % and iowait, computed from consecutive scrapes; shows "warming up" until the second sample, normally within 30 seconds of startup
- Pressure stall percentages (CPU / IO / memory PSI)
- Memory usage, buffers/cache, swap
- Load averages colored relative to core count
- Filesystem usage, including ZFS mounts; skips tmpfs, fuse, and overlay mounts
- Network throughput; skips loopback and Proxmox guest interfaces (`veth*`, `tap*`, `fwbr*`)
- hwmon temperature sensors
- OS, kernel, and uptime

The **Storage** page (`/storage`, also available at `/pools`) shows ZFS pools and SMART disks independently. A host does not need ZFS to show disk health. Explicitly enabled exporters retain their error states when unavailable.

## History

When `history.enabled: true`, HostGlance records available pool, disk, and system metrics to a local [bbolt](https://github.com/etcd-io/bbolt) database, sampling every `history.record_interval` (defaults to `refresh`).

Charts live at **`/history`**; the History tab appears in the topbar once enabled. Previously recorded data remains available when an exporter disappears, until it expires under the retention setting.

**Recorded metrics:**

| Series                                                                | Description                               |
| --------------------------------------------------------------------- | ----------------------------------------- |
| `pool/{name}/used_pct`                                                | Pool used %                               |
| `pool/{name}/alloc_bytes`                                             | Pool allocated bytes                      |
| `pool/{name}/free_bytes`                                              | Pool free bytes                           |
| `disk/{dev}/temp_c`                                                   | Disk temperature °C                       |
| `disk/{dev}/wear_pct`                                                 | NVMe percentage used (wear)               |
| `disk/{dev}/wear_lvl`                                                 | SATA SSD wear leveling count              |
| `disk/{dev}/pow_hrs`                                                  | Power-on hours                            |
| `system/node/cpu_pct`                                                 | CPU busy % (node_exporter)                |
| `system/node/iowait_pct`                                              | CPU iowait %                              |
| `system/node/mem_used_pct`                                            | Memory used % (node_exporter)             |
| `system/node/swap_used_pct`                                           | Swap used %                               |
| `system/node/load1`, `load5`, `load15`                                | 1-, 5-, and 15-minute load averages       |
| `system/node/pressure_cpu_pct`, `pressure_io_pct`, `pressure_mem_pct` | CPU, IO, and memory pressure %            |
| `fs/{mount}/used_pct`                                                 | Filesystem usage %, excluding boot mounts |
| `net/{interface}/rx_bps`, `tx_bps`                                    | Receive and transmit bytes per second     |
| `temp/{chip label}/temp_c`                                            | hwmon sensor temperature °C               |

HostGlance prunes data older than the retention window. Each data point stores 8 bytes of values: 30 days at a 5-minute interval across 50 disks × 4 metrics ≈ 14 MB raw, around 35 MB on disk with bbolt key and page overhead.

**Docker:** uncomment the `./data:/data` volume in `docker-compose.yml` and set `history.path: /data/history.db` in your config.

## Hot Reload

Edits to host lists, exporter modes and URLs, `refresh`, and `debug` reload automatically. To trigger a reload manually:

```bash
kill -HUP $(pgrep hostglance)
```

Changes to `addr`, `cache_ttl`, `trusted_proxies`, or history settings require a restart; a reload logs a warning naming any such changed setting.

## Docker

Edit `config.yaml` (copy from `config.yaml.example`) before starting the stack:

```bash
docker compose up -d
```

Hostnames and exporter URLs must be reachable from the container. `localhost` refers to the container itself.

## API

| Route                                           | Purpose                                                                        |
| ----------------------------------------------- | ------------------------------------------------------------------------------ |
| `GET /`, `GET /system`                          | System overview for every configured host                                      |
| `GET /storage`, `GET /pools`                    | Available or required ZFS and SMART sections                                   |
| `GET /history`                                  | History charts; requires `history.enabled: true`                               |
| `GET /api/metrics`                              | Host metrics and exporter availability                                         |
| `GET /api/system`                               | System metrics for hosts with an available or explicitly enabled node exporter |
| `GET /api/history/series`                       | List recorded series; history only                                             |
| `GET /api/history/query?key=&from=&to=&bucket=` | Query time-series data; history only                                           |
| `GET /api/health/:label`                        | Host health based on exporter requirements and pool health                     |
| `GET /api/health/:label/:pool`                  | Pool health                                                                    |
| `GET /health`                                   | App liveness, independent of exporter availability                             |

`/api/metrics` includes an `exporters` object on each host with `node`, `zfs`, and `smartctl` statuses. Each status reports its `mode` and `available` state, plus an `error` when a required exporter fails. Automatic absence does not produce an error.

Scrape URLs are not exposed as exporter endpoints in the UI or API. Labels are public: legacy entries that defaulted their label to a URL continue to expose that label. SSE connections on `/events` cap at 64 concurrent clients.

### Health Checks

Health checks suit monitoring tools like Uptime Kuma. `GET /api/health/:label` returns:

| Condition                                                                    | HTTP status | Meaning                                            |
| ---------------------------------------------------------------------------- | ----------- | -------------------------------------------------- |
| Initial collection is still pending                                          | `503`       | `status: unknown`, `reason: discovery_pending`     |
| Any `enabled` exporter is unavailable                                        | `503`       | A configured requirement failed                    |
| Available ZFS data contains an unhealthy pool or exceeds `max_usage_percent` | `503`       | Storage is unhealthy, regardless of exporter mode  |
| ZFS is `enabled` but reports no pools                                        | `503`       | Required ZFS storage is missing (`no_pools`)       |
| No exporters are available and none are required                             | `200`       | `status: unknown`, `reason: no_exporters_detected` |
| Available exporters satisfy the checks above                                 | `200`       | `status: up`                                       |

An unknown host status is neutral; exporter discovery cannot prove whether the machine itself is healthy.

`GET /api/health/:label/:pool` also returns `503` with `status: unknown`, `reason: discovery_pending` while initial collection is pending. After collection, it returns `503` if ZFS is unavailable, the pool is missing or unhealthy, or its usage exceeds `max_usage_percent`. A failure in an unrelated node or SMART exporter does not fail a healthy pool check.

Examples: `GET /api/health/node-1` and `GET /api/health/node-1/tank`.

## Development

Run the test suite, including race detection and shuffled test order:

```bash
go test -race -shuffle=on ./...
```

For a local smoke test, configure a host with `node_exporter` only and use a short `refresh` interval. Confirm that the System metrics appear without a ZFS error. Stop and restart the exporter to check automatic disappearance and rediscovery, then set its mode to `enabled` and stop it again to check the visible error and `503` host health response.
