# smart-git-proxy

HTTP smart Git mirror proxy for `git fetch`/`git clone` over smart HTTP. Maintains local bare repo mirrors to serve multiple clients efficiently. Designed to run inside a trusted VPC; plain HTTP listener by default.

## Benefits

- **Faster checkouts** - Clones served from local NVMe storage instead of fetching from GitHub each time
- **Lower bandwidth costs** - Upstream fetches only happen once per repo; all clients share the same mirror
- **Reduced GitHub API pressure** - Fewer upstream requests means less rate limiting risk
- **Resilience to upstream outages** - Cached repos remain available even if GitHub is slow or down
- **Shared objects across refs** - Unlike HTTP caching, git objects are shared even when clients request different branches/tags

## Installation

Download pre-built binaries from [Releases](https://github.com/runs-on/smart-git-proxy/releases).

### AWS CloudFormation (recommended for RunsOn)

Deploy the proxy in your RunsOn VPC with auto-healing and DNS-based service discovery:

```
https://runs-on.s3.eu-west-1.amazonaws.com/cloudformation/smart-git-proxy.yaml
```

The stack creates:
- An ASG with a single instance (auto-healing)
- A Route53 private hosted zone for DNS resolution (`smart-git-proxy.runs-on.internal`)
- Automatic instance registration/deregistration

Then configure your workflows:

```yaml
steps:
  - name: Setup proxy
    run: |
      AUTH=$(echo -n "x-access-token:${{ secrets.GITHUB_TOKEN }}" | base64)
      git config --global url."http://smart-git-proxy.runs-on.internal:8080/github.com/".insteadOf "https://github.com/"
      git config --global "http.http://smart-git-proxy.runs-on.internal:8080/.extraheader" "AUTHORIZATION: basic $AUTH"

  - name: Clone repo
    uses: actions/checkout@v4
```

### Debian/Ubuntu (.deb)
```bash
# Download and install (auto-creates user, enables systemd service)
curl -LO https://github.com/runs-on/smart-git-proxy/releases/latest/download/smart-git-proxy_<version>_linux_amd64.deb
sudo dpkg -i smart-git-proxy_*.deb

# Edit config
sudo vi /etc/smart-git-proxy/env

# Restart to apply changes
sudo systemctl restart smart-git-proxy
```

### RHEL/Amazon Linux (.rpm)
```bash
# Download and install (auto-creates user, enables systemd service)
curl -LO https://github.com/runs-on/smart-git-proxy/releases/latest/download/smart-git-proxy_<version>_linux_arm64.rpm
sudo yum install -y ./smart-git-proxy_*.rpm

# Edit config
sudo vi /etc/smart-git-proxy/env

# Restart to apply changes
sudo systemctl restart smart-git-proxy
```

## How it works

1. Client requests `info/refs` via proxy → proxy creates/syncs a bare mirror from upstream
2. Client fetches pack data → proxy serves directly from local mirror
3. Subsequent requests for same repo reuse the mirror (no upstream fetch if recent)
4. Multiple clients requesting different refs/branches share the same mirror

**Key benefit**: Unlike HTTP response caching, the mirror-based approach shares git objects across all clients, even when they request different refs.

## Prereqs
- Go 1.25+ (toolchain pinned in `go.mod`; `.mise.toml` can install Go for you)
- `mise` for toolchain setup
- `git` installed on the proxy server

## Install tooling
```bash
mise install
```

## Build / test
```bash
# format & tidy
make fmt tidy

# run tests
make test

# build binary
make build   # produces bin/smart-git-proxy
```

## Run locally
Minimal run:
```bash
MIRROR_DIR=/tmp/git-mirrors \
LISTEN_ADDR=:8080 \
ALLOWED_UPSTREAMS=github.com \
./bin/smart-git-proxy
```

Expose metrics/health via defaults: `/metrics`, `/healthz`.

## Using the proxy (Git)
This proxy is not a generic CONNECT proxy; it expects direct smart-HTTP paths. Do **not** use `https_proxy` (Git will try CONNECT). Use URL rewriting instead.

URL format: `http://proxy/{host}/{owner}/{repo}/...` - the hostname (e.g. `github.com`) must be in the path.

### Quick test (no auth)
Repository: `https://github.com/runs-on/runs-on`
```bash
# Rewrite GitHub URLs to go through proxy
git -c url."http://localhost:8080/github.com/".insteadOf="https://github.com/" \
    clone https://github.com/runs-on/runs-on /tmp/runs-on
```
First run creates mirror from upstream; subsequent clones serve from local mirror.

### With auth
If the upstream requires a token, either:
1. Pass-through: use normal Git credentials (`AUTH_MODE=pass-through`)
2. Static token: proxy injects token upstream (`AUTH_MODE=static STATIC_TOKEN=ghp_xxx`)

```bash
AUTH_MODE=static STATIC_TOKEN=ghp_your_token_here ./bin/smart-git-proxy
git -c url."http://localhost:8080/github.com/".insteadOf="https://github.com/" \
    ls-remote https://github.com/runs-on/runs-on
```

## systemd deployment

The `.deb` and `.rpm` packages automatically install and start the systemd service. For manual setup:
- Unit file: `scripts/smart-git-proxy.service`
- Example env file: `scripts/env.example`
- Config location: `/etc/smart-git-proxy/env`
- Default mirror dir: `/var/lib/gitproxy/mirrors` (override with `MIRROR_DIR` for NVMe)

## Configuration

All config via environment variables (or flags):

| Variable | Default | Description |
|----------|---------|-------------|
| `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `MIRROR_DIR` | `/mnt/git-mirrors` | Directory for bare git mirrors |
| `MIRROR_MAX_SIZE` | `80%` | Max cache size: absolute (`200GiB`, `500GB`) or percentage (`80%`). LRU eviction when exceeded |
| `SYNC_STALE_AFTER` | `2s` | Sync mirror if last sync older than this |
| `ALLOWED_UPSTREAMS` | `github.com` | Comma-separated allowed upstream hosts |
| `AUTH_MODE` | `pass-through` | `pass-through`, `static`, or `none` |
| `STATIC_TOKEN` | - | Token for `AUTH_MODE=static` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

## Architecture

```
┌─────────┐     ┌─────────────────────────────────────────────┐
│ Client  │────▶│              smart-git-proxy                │
└─────────┘     │  ┌─────────┐   ┌──────────────────────────┐ │
                │  │ Handler │──▶│ Mirror Manager           │ │
                │  └─────────┘   │  - EnsureRepo()          │ │
                │       │        │  - singleflight sync     │ │
                │       ▼        │  - per-repo locking      │ │
                │  ┌─────────┐   └──────────────────────────┘ │
                │  │git serve│◀──────────────────────────────┘│
                │  │(local)  │                                │
                └──┴─────────┴────────────────────────────────┘
                        │
                        ▼
              /mnt/git-mirrors/
                github.com/
                  runs-on/
                    runs-on.git/    # bare mirror
```

## Notes / limits
- Only smart HTTP upload-pack is handled (`info/refs?service=git-upload-pack`, `git-upload-pack` POST).
- Mirrors are synced on `info/refs` requests if stale (configurable via `SYNC_STALE_AFTER`).
- Concurrent requests for same repo share a single sync operation (singleflight).
- Does **not** support `https_proxy` / CONNECT tunneling (use `url.insteadOf` instead).
- LRU cache eviction removes least recently used mirrors when disk usage exceeds `MIRROR_MAX_SIZE`.
- Mirror cleanup (gc, prune) is handled by git's normal mechanisms.
