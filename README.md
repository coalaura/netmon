# Netmon

A lightweight, dark-themed network monitoring dashboard for Windows and Linux. Monitoring starts automatically and follows network changes, with no configuration file, interface selection or server setup.

## Get started

Download a prebuilt binary from [GitHub Releases](https://github.com/coalaura/netmon/releases) and launch it in a desktop session. Close the window to stop monitoring and flush the log.

### Build from source

Requires Go 1.27.2 or newer. From the repository directory:

```sh
go build .
```

No CGO is required. Run the resulting `netmon.exe` on Windows or `./netmon` on Linux.

## What it monitors

- **ICMP:** gateway latency and remote pings to `9.9.9.9`, `1.1.1.1` and `8.8.8.8`.
- **DNS:** resolution of `example.com`, `google.com` and `cloudflare.com` through the interface's resolver, `9.9.9.9` and the gateway when it supports DNS.
- **Interface:** the OS-selected interface for reaching `9.9.9.9`, its source address, link state and reported speed, gateway neighbor/ARP state and default-route availability.
- **Traceroute:** a background trace to `9.9.9.9` approximately every 20 seconds, with per-hop addresses and RTTs, route changes and a one-hour destination hop-count chart.
- **History:** five-minute latency charts, rolling failure rates, recent state changes and continuous CSV logging.

ICMP and DNS checks run concurrently, roughly once per second, with an 800 ms deadline per probe. Netmon currently monitors IPv4 only.

## Reading the dashboard

- Remote ICMP and DNS charts show the **mean of successful replies**. Read these alongside success counts and per-target results: losing a slower target can lower the mean even when connectivity worsens.
- Amber chart bands indicate timeouts or partial results, red indicates errors and orange indicates permission problems. Samples with no successful replies leave gaps rather than plotting zero latency.
- Traceroute reports a destination hop count only when the destination responds. `*` means no reply; hop RTT is the round trip to that router, not the latency of an individual link.
- Link speed is adapter-reported, not measured throughput. A stale ARP entry is normal. ICMP responses can be filtered or rate-limited, so compare them with DNS and interface status.

## CSV logs

Each launch creates a unique `netmon-<timestamp>.csv`. The dashboard shows its path and provides an **Open CSV** button.

| Platform | Log directory |
| --- | --- |
| Windows | `%AppData%\netmon\logs\` |
| Linux | `$XDG_CONFIG_HOME/netmon/logs/` or `~/.config/netmon/logs/` |

Logs include network state, monitor summaries, individual ping and DNS results and traceroute summaries and hops. Filter by `record_type` when calculating statistics to avoid counting both summaries and individual results.

Rows are flushed after each sample or completed trace. Logging errors appear in the dashboard while monitoring continues. Logs are not automatically rotated or deleted.

## Platform notes

- **Windows:** uses native network discovery and ICMP traceroute.
- **Linux:** uses netlink and sysfs for network discovery and systemd-resolved for per-interface DNS with `/etc/resolv.conf` as a fallback. ICMP tries unprivileged ping sockets, then raw sockets; if both are denied, the dashboard reports a permission error while other monitors continue. Traceroute uses UDP without requiring raw-socket privileges. **Open CSV** requires `xdg-open`.

DNS servers and local stubs may return cached answers. Windows ICMP and Linux UDP traceroutes can follow different paths or encounter different filtering.
