# HostGlance

HostGlance is a dashboard for the system and storage health of your hosts. You give it hostnames or IP addresses. It finds [node_exporter](https://github.com/prometheus/node_exporter), [pdf/zfs_exporter](https://github.com/pdf/zfs_exporter), and [smartctl_exporter](https://github.com/prometheus-community/smartctl_exporter) on each host and shows the metrics that they report.

ZFS is optional. A host that runs only node_exporter, or only smartctl_exporter, works on its own.

## Run

To run from source, you need Go 1.26.2 or later.

Create a `config.yaml` file that lists the hosts to monitor:

```yaml
hosts:
  - nas.home
  - server02.home
  - 192.168.1.20
```

Start the server:

```bash
go run . serve --config config.yaml
```

Open `http://localhost:8054`. The System page shows every host in the configuration. The Storage page appears when a host reports ZFS or SMART metrics, or when you set one of these exporters to `mode: enabled`. A host with no exporters shows "No exporters detected."

To test the configuration without starting the server, run `go run . check --config config.yaml`. The command connects to each host one time and prints the exporters that it found. It exits with a non-zero code if an exporter with `mode: enabled` does not respond.

For all settings, copy [config.yaml.example](config.yaml.example) to `config.yaml` and change the hosts.

### Flags

- `--config`: The configuration file. By default, HostGlance reads `./config.yaml`, then `~/.config/hostglance/config.yaml`.
- `--hosts`: Hostnames or IP addresses, separated by commas or given more than one time. HostGlance finds the exporters on each host.
- `--endpoints`: The old format, a list of ZFS exporter URLs. You cannot use it together with `hosts`.
- `--addr`: The address to listen on. The default is `:8054`.
- `--refresh`: The time in seconds between two metric collections. The default is `300`. In the configuration file, you can also write a duration such as `"5m"`.
- `--debug`: Write debug messages to the log.
- `--trusted-proxies`: The IP addresses of reverse proxies whose forwarded headers HostGlance accepts.
- `--max-usage-percent`: The pool usage above which a health check fails. The default is `0`, which turns the check off.
- `--log-format`: The log format, `text` or `json`. The default is `text`.
- `--history-enabled`: Record metrics for the history charts. The default is `false`.
- `--history-path`: The file for the history database. The default is `./data/history.db`.
- `--history-retention`: How long HostGlance keeps history, for example `720h` for 30 days. The default is `720h`.
- `--history-record-interval`: The time between two history samples, for example `5m`. The default is the `--refresh` value.

For example, `go run . serve --hosts nas.home,server02.home` monitors two hosts. The environment variable `HOSTGLANCE_HOSTS=nas.home,server02.home` gives the same list. For a nested setting, use an underscore, for example `HOSTGLANCE_HISTORY_ENABLED=true`.

A flag overrides an environment variable, and an environment variable overrides the configuration file. HostGlance does not start if the configuration has an unknown setting, a `log_format` other than `text` or `json`, or a `max_usage_percent` outside 0 to 100. Use only one host format, `hosts` or `endpoints`, in all sources together.

## Configuration

```yaml
addr: ":8054"
refresh: 300
cache_ttl: 30 # reuse collected metrics for 30 seconds
max_usage_percent: 90 # a health check fails if a pool is more than 90% full
log_format: "text" # "text" or "json"
debug: false
trusted_proxies: [] # for example ["127.0.0.1", "100.64.0.0/10"]

history:
  enabled: false
  path: "./data/history.db"
  retention: "720h" # 30 days, as a Go duration
  record_interval: "5m" # the default is the refresh value

hosts:
  - server02.home
  - address: nas.home
    label: node-1
    location: Singapore
    exporters:
      zfs:
        mode: enabled # required: an error shows if this exporter does not respond
      node:
        mode: auto # optional: this is also the default
      smartctl:
        mode: disabled # never connect to this exporter
  - address: nas-vm.home
    parent: node-1 # this VM runs on node-1
  - address: "2001:db8::20"
    label: node-2
    exporters:
      node:
        url: "https://metrics.example.net/server02/node/metrics"
        # A custom URL alone keeps mode: auto.
```

Each host is a hostname, an IP address, or an object. The `address` field is a hostname or IP address without a scheme, port, or path. To change the port, scheme, or path, set the `url` field of the exporter. You can write IPv6 addresses without brackets.

The `label` field is the name that HostGlance shows. It is the address by default, and each label must be unique. Health URLs and history data use the label, so do not change it after you start to record history.

If a host is a VM or a container that runs on another host, set `parent` to the label of that other host, for example `parent: pve01`. If the other host has a `pve` exporter, HostGlance can find the parent itself (see "Proxmox guests"). HostGlance shows the guest below its parent on the System page. The fleet totals for cores, memory, and CPU do not include guests, because the parent already counts their resources. A guest cannot have its own guests.

### Exporter discovery

Each exporter that you do not configure uses `mode: auto`. The `pve` exporter is the exception, because it needs a `url`. HostGlance connects to the default URL of each exporter and looks for the metric names that the exporter writes:

| Exporter   | Default URL                  | Data                                                  |
| ---------- | ---------------------------- | ----------------------------------------------------- |
| `node`     | `http://<host>:9100/metrics` | CPU, memory, load, network, filesystems, temperatures |
| `zfs`      | `http://<host>:9134/metrics` | ZFS pools and datasets                                |
| `smartctl` | `http://<host>:9633/metrics` | Disk health, temperature, wear                        |
| `pve`      | none, set `url`              | Proxmox VMs and LXC containers                        |

HostGlance does not scan your network and does not install exporters. Each exporter must run already, and HostGlance must be able to connect to it. To use a different port, scheme, or path, or a reverse proxy, set the `url` field.

| Mode       | What HostGlance does                                                                                                              |
| ---------- | --------------------------------------------------------------------------------------------------------------------------------- |
| `auto`     | Collects metrics when the exporter responds. If the exporter does not respond, HostGlance hides its section and shows no error.   |
| `enabled`  | Requires the exporter. The section stays visible, and an error shows if the exporter does not respond or returns unknown metrics. |
| `disabled` | Never connects to the exporter.                                                                                                   |

HostGlance looks for exporters at startup and again at each refresh. If you start an exporter later, its metrics appear at the next refresh. An `auto` exporter that stops responding does not mark the host as unhealthy. When one exporter fails, the other exporters on the host continue to work. An unhealthy pool fails the health check in every mode, `auto` included.

### Proxmox guests

The `pve` exporter is [prometheus-pve-exporter](https://github.com/prometheus-pve/prometheus-pve-exporter). It reads the Proxmox API, so it can run on another server. For this reason, it has no default URL, and HostGlance does not connect to it until you set `url`.

Set the URL on the entry of the Proxmox host. The `target` parameter is the address of the Proxmox node, and `module` is the section in the `pve.yml` file of the exporter:

```yaml
- address: 100.64.0.27
  label: PVE01
  exporters:
    pve:
      mode: enabled
      url: "http://127.0.0.1:9221/pve?target=100.64.0.27&module=pve01&cluster=1&node=0"
```

The query needs `cluster=1`, because the guest metrics come from the cluster collectors. `node=0` turns off the node collectors, which HostGlance does not use.

The card of the Proxmox host then lists each VM and container with its state, CPU, memory, and uptime. HostGlance skips templates. A stopped guest does not fail the health check. For a VM, the memory value comes from Proxmox, and it includes the page cache of the guest.

The guest list also shows:

- A "no backup" flag on each guest that no Proxmox backup job includes, and the number of these guests in the list header.
- "stopped, starts at boot" for a stopped guest that is set to start at boot. This usually means that the guest failed.
- The lock, such as "backup lock", while Proxmox backs up, migrates, or snapshots the guest.
- The disk use of each LXC container. A VM reports its disk use only when it runs the QEMU guest agent.

The host card shows the Proxmox version. The Storage page shows the Proxmox storages of the host, such as LVM thin pools, Proxmox Backup Server datastores, NFS shares, and directories. It skips `zfspool` storages when the ZFS exporter responds, because the ZFS pools already show these.

HostGlance also joins the guest list with the other hosts in the configuration. If the label or the reported hostname of a host is the same as a guest name, HostGlance:

- Sets that Proxmox host as the parent. You do not have to write `parent`. A `parent` that you write takes priority.
- Shows the guest number on the card of the guest, for example "VM 502 on PVE01".
- Links the guest row in the list of the parent to the card of the guest.
- Shows "Proxmox reports this VM as stopped." on the card when Proxmox reports the guest as stopped.

The names must match. Case does not matter.

### Move from the old endpoints format

HostGlance still reads the `endpoints` format and the `--endpoints` flag. In that format, the `url` of each entry is a required ZFS exporter. The `node_exporter_url` and `smartctl_url` fields are optional. HostGlance does not look for exporters that an entry does not list.

Some old ZFS URLs also returned SMART metrics in the same response. HostGlance still reads those SMART metrics for `endpoints` entries. In that case, the SMART status shows `mode: auto` and `available: true`, even without a `smartctl_url`. A `hosts` entry collects each exporter on its own, and `smartctl.mode: disabled` always stops SMART collection.

For example, change this old entry:

```yaml
endpoints:
  - url: "http://nas.home:9134/metrics"
    label: node-1
    location: Singapore
    node_exporter_url: "http://nas.home:9100/metrics"
```

to this entry:

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

The new entry keeps ZFS required and does not collect SMART metrics. To collect SMART metrics, remove the `smartctl` lines. To make ZFS optional, change its mode to `auto`. If the old entry had custom exporter URLs, copy them to the `url` fields. If the old ZFS URL also returned SMART metrics, set the same URL under `exporters.smartctl` with `mode: auto`.

Remove the old `endpoints` setting, the `--endpoints` flag, and the `HOSTGLANCE_ENDPOINTS` environment variable. HostGlance does not start when both formats are present. To keep your history, keep the same labels and the same `history.path`. If an old entry had no `label`, its label was its ZFS URL. Copy that URL to `label` to keep the history of that host.

The program is `hostglance`, and the container image is `ghcr.io/crazyuploader/hostglance`. Environment variables start with `HOSTGLANCE_`, and the default configuration directory is `~/.config/hostglance`.

## System and Storage pages

The System page (`/`, also at `/system`) shows each host and its system metrics:

- CPU busy and iowait percentages. HostGlance needs two samples to calculate them, so they show "warming up" for about 30 seconds after startup.
- Pressure stall percentages for CPU, I/O, and memory.
- Memory, buffers and cache, and swap.
- Load averages, colored against the number of cores.
- Filesystem usage. HostGlance skips tmpfs, fuse, and overlay mounts. It shows ZFS mounts only when the host has no ZFS exporter, because the Storage page shows the pools.
- Network throughput. HostGlance skips the loopback interface and Proxmox guest interfaces (`veth*`, `tap*`, `fwbr*`). Container and overlay interfaces, such as `br-*`, `docker0`, and `cali*`, go into a list that you can open.
- hwmon temperature sensors.
- The OS, the kernel, and the uptime.

The Storage page (`/storage`, also at `/pools`) shows ZFS pools and SMART disks. The two are separate, so a host without ZFS can still show disk health. An exporter with `mode: enabled` keeps its error visible when it does not respond.

## History

When you set `history.enabled: true`, HostGlance records pool, disk, and system metrics in a local [bbolt](https://github.com/etcd-io/bbolt) database. It takes one sample at each `history.record_interval`, which is the `refresh` value by default.

The charts are at `/history`. The History tab appears in the top bar when history is on. If an exporter stops, its recorded data stays until it is older than the retention period.

HostGlance records these series:

| Series                                                                | Description                             |
| --------------------------------------------------------------------- | --------------------------------------- |
| `pool/{name}/used_pct`                                                | Pool used %                             |
| `pool/{name}/alloc_bytes`                                             | Pool allocated bytes                    |
| `pool/{name}/free_bytes`                                              | Pool free bytes                         |
| `disk/{dev}/temp_c`                                                   | Disk temperature °C                     |
| `disk/{dev}/wear_pct`                                                 | NVMe percentage used (wear)             |
| `disk/{dev}/wear_lvl`                                                 | SATA SSD wear leveling count            |
| `disk/{dev}/pow_hrs`                                                  | Power-on hours                          |
| `system/node/cpu_pct`                                                 | CPU busy %                              |
| `system/node/iowait_pct`                                              | CPU iowait %                            |
| `system/node/mem_used_pct`                                            | Memory used %                           |
| `system/node/swap_used_pct`                                           | Swap used %                             |
| `system/node/load1`, `load5`, `load15`                                | 1-, 5-, and 15-minute load averages     |
| `system/node/pressure_cpu_pct`, `pressure_io_pct`, `pressure_mem_pct` | CPU, I/O, and memory pressure %         |
| `fs/{mount}/used_pct`                                                 | Filesystem usage %, without boot mounts |
| `net/{interface}/rx_bps`, `tx_bps`                                    | Receive and transmit bytes per second   |
| `temp/{chip label}/temp_c`                                            | hwmon sensor temperature °C             |

HostGlance deletes data that is older than the retention period. Each data point uses 8 bytes. For example, 50 disks with 4 metrics each, sampled every 5 minutes for 30 days, use about 14 MB of data and about 35 MB on disk.

To keep history in Docker, remove the comment from the `./data:/data` volume in `docker-compose.yml`. Then set `history.path: /data/history.db` in the configuration.

## Reload the configuration

HostGlance reloads the configuration when the file changes. A reload applies changes to hosts, exporter modes and URLs, `refresh`, and `debug`. To start a reload yourself, send `SIGHUP`:

```bash
kill -HUP $(pgrep hostglance)
```

Changes to `addr`, `cache_ttl`, `trusted_proxies`, or history settings apply only after a restart. If a reload finds a change to one of them, HostGlance writes a warning to the log.

## Docker

Copy `config.yaml.example` to `config.yaml` and change the hosts. Then start the stack:

```bash
docker compose up -d
```

The container must be able to connect to each host and exporter URL. Inside the container, `localhost` is the container itself, not the Docker host.

## API

| Route                                           | Purpose                                                               |
| ----------------------------------------------- | --------------------------------------------------------------------- |
| `GET /`, `GET /system`                          | The System page for all hosts                                         |
| `GET /storage`, `GET /pools`                    | The Storage page for hosts with ZFS or SMART data                     |
| `GET /history`                                  | History charts. Needs `history.enabled: true`.                        |
| `GET /api/metrics`                              | Host metrics and exporter status                                      |
| `GET /api/system`                               | System metrics for hosts whose node exporter responds or is `enabled` |
| `GET /api/history/series`                       | The list of recorded series. Needs history.                           |
| `GET /api/history/query?key=&from=&to=&bucket=` | Data for one series. Needs history.                                   |
| `GET /api/health/:label`                        | Host health, from exporter requirements and pool health               |
| `GET /api/health/:label/:pool`                  | Pool health                                                           |
| `GET /api/health/:label/disk/:disk`             | Disk health, by serial number or by-id device name                    |
| `GET /health`                                   | Shows that the app runs. It does not depend on the exporters.         |

In `/api/metrics`, each host has an `exporters` object with a status for `node`, `zfs`, and `smartctl`. Each status has a `mode` and an `available` value. It also has an `error` when an `enabled` exporter fails. An `auto` exporter that does not respond has no error.

The UI and the API do not show exporter URLs. They do show labels, so an old `endpoints` entry whose label is its URL still shows that URL. The `/events` stream accepts at most 64 clients at the same time.

### Health checks

Monitoring tools such as Uptime Kuma can use the health checks. `GET /api/health/:label` returns:

| Condition                                                      | HTTP status | Meaning                                            |
| -------------------------------------------------------------- | ----------- | -------------------------------------------------- |
| The first collection did not finish yet                        | `503`       | `status: unknown`, `reason: discovery_pending`     |
| An `enabled` exporter does not respond                         | `503`       | A required exporter failed                         |
| A pool is unhealthy, or its usage is above `max_usage_percent` | `503`       | Storage is unhealthy, in every exporter mode       |
| ZFS is `enabled` but reports no pools                          | `503`       | Required ZFS storage is missing (`no_pools`)       |
| A Proxmox storage is inactive                                  | `503`       | `reason: storage_inactive`                         |
| A Proxmox storage is above `max_usage_percent`                 | `503`       | `reason: storage_over_threshold`                   |
| No exporter responds, and none is required                     | `200`       | `status: unknown`, `reason: no_exporters_detected` |
| All the checks above pass                                      | `200`       | `status: up`                                       |

The Proxmox storage checks use the storages that the Storage page shows, so a `zfspool` storage is not checked twice when the ZFS exporter responds. The response names the storages in `inactive_storages` or `over_threshold_storages`.

An `unknown` status is neutral. The exporters alone cannot show whether the machine is healthy.

`GET /api/health/:label/:pool` also returns `503` with `status: unknown` and `reason: discovery_pending` before the first collection finishes. After that, it returns `503` if ZFS does not respond, if the pool is missing or unhealthy, or if the pool usage is above `max_usage_percent`. A failure of the node or SMART exporter does not fail the check of a healthy pool.

`GET /api/health/:label/disk/:disk` finds the disk in the SMART data by its serial number or its device name, for example `SN0001` or `ata-EXAMPLE_DISK_SN0001`. It returns `503` with one of these reasons:

- `exporter_unavailable`: the SMART exporter is `enabled` and does not respond.
- `disk_not_found`: the SMART exporter does not report the disk, or node_exporter does not list its serial number in `node_disk_info`. For example, the disk is removed or offline.
- `disk_unreachable`: smartctl cannot open or read the disk (exit status bits 0 to 2).
- `smart_failed`: the SMART health check of the disk fails.
- `presence_unknown` (with `status: unknown`): node_exporter does not respond or reports no serial numbers in `node_disk_info`, so HostGlance cannot confirm that the disk is connected.

Other exit status bits, such as errors in the error log, do not fail the check. The Storage page shows them.

smartctl_exporter keeps the last data of a removed disk until the exporter restarts. For this reason, the check also uses `node_disk_info` from node_exporter, which the kernel updates at each scrape. The disk check therefore needs both exporters. Without node_exporter serial numbers, it fails with `presence_unknown` instead of trusting old SMART data.

For example: `GET /api/health/node-1`, `GET /api/health/node-1/tank`, and `GET /api/health/node-1/disk/SN0001`.

## Development

Run the tests with the race detector and in random order:

```bash
go test -race -shuffle=on ./...
```

To test by hand, follow these steps:

1. Configure one host that runs only `node_exporter`, and set a short `refresh` interval.
2. Make sure that the System page shows the metrics and no ZFS error.
3. Stop the exporter, then start it again. Make sure that its metrics go away and come back.
4. Set the node exporter to `mode: enabled` and stop it. Make sure that the page shows an error and that the host health check returns `503`.
