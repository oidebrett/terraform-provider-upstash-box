# terraform-provider-upstash-box

Terraform provider for [Upstash Box](https://upstash.com/docs/box), built for
manidae-cloud / HostMyAgents. It wraps the Box REST API
(`https://us-east-1.box.upstash.com/v2/box`, header `X-Box-Api-Key`) the same
way `terraform-provider-komodo` wraps Komodo.

| Resource | What it does |
|---|---|
| `upstashbox_box` | Creates a box: `size` (small/medium/large), `runtime`, `keep_alive` (always-on, paid plan) or serverless (default), `env`, `labels`, `init_command` (runs on every container start; updated in place). Everything else forces replacement. |
| `upstashbox_exec` | Runs a script once as `boxuser` — the replacement for cloud-init `user_data`. Detached + polled, so long installs don't hang on one HTTP request. Re-runs when `script`, `box_id` or `triggers` change. |
| `upstashbox_public_url` | Exposes a port at `https://<box>-<port>.preview.box.upstash.com` (WebSockets work; traffic wakes a paused box). Optional Upstash bearer/basic auth. |

Provider config: `api_key` (or `UPSTASH_BOX_API_KEY`), `base_url` (or
`UPSTASH_BOX_BASE_URL`). Use a **Box** API key; the Redis developer key gets 401.

## What a box is (from the 2026-11-03 spike)

- Docker (runc) container, **arm64**, Debian 12, user `boxuser` with passwordless `sudo -n`.
- No Docker-in-Docker, Podman, udocker, systemd, `/dev/kvm`, `/dev/net/tun` — root has
  almost no capabilities and user namespaces are blocked. Run processes directly.
- Start long-running processes from `init_command` with `nohup`.
- Inside a box, `pgrep -f`/`pkill -f <name>` can match the shell running the
  command and kill it. Put such logic in a script file.
- Serverless billing is real CPU time ($0.10 per CPU-hour on small); keep-alive is
  $8/$16/$32 a month.

## Build and install locally

```bash
go test ./...
go build -o bin/terraform-provider-upstash-box .
d=~/.terraform.d/plugins/registry.example.com/mattercoder/upstash-box/0.1.0/$(go env GOOS)_$(go env GOARCH)
mkdir -p "$d" && cp bin/terraform-provider-upstash-box "$d/"
```

See `examples/openclaw` for a standalone OpenClaw box.
