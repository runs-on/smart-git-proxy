# smart-git-proxy

HTTP(S) smart Git caching proxy for `git fetch`/`git clone` over smart HTTP. Designed to run inside a trusted VPC; plain HTTP listener by default, TLS optional if you front it with a load balancer.

## Prereqs
- Go 1.25+ (toolchain pinned in `go.mod`; `.mise.toml` can install Go for you)
- `mise` for toolchain setup

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
Minimal run with local cache:
```bash
CACHE_DIR=/tmp/git-cache \
LISTEN_ADDR=:8080 \
ALLOWED_UPSTREAMS=github.com \
AUTH_MODE=pass-through \
./bin/smart-git-proxy
```

Expose metrics/health via defaults: `/metrics`, `/healthz`.

## Using the proxy (Git)
This proxy is not a generic CONNECT proxy; it expects direct smart-HTTP paths so it can cache. Do **not** use `https_proxy` (Git will try CONNECT and you'll see 301/CONNECT errors). Use URL rewriting instead.

URL format: `http://proxy/{host}/{owner}/{repo}/...` - the hostname (e.g. `github.com`) must be in the path.

### Quick test (no auth)
Repository: `https://github.com/runs-on/runs-on`
```bash
# Rewrite GitHub URLs to go through proxy
git -c url."http://localhost:8080/github.com/".insteadOf="https://github.com/" \
    clone https://github.com/runs-on/runs-on /tmp/runs-on
```
First run populates cache; repeat runs should hit locally.

### With auth
If the upstream requires a token, either rely on your normal Git creds (pass-through), or run the proxy with a static token:
```bash
AUTH_MODE=static STATIC_TOKEN=ghp_your_token_here ./bin/smart-git-proxy
git -c url."http://localhost:8080/github.com/".insteadOf="https://github.com/" \
    ls-remote https://github.com/runs-on/runs-on
```
Static mode injects `Authorization: Bearer $STATIC_TOKEN` upstream.

## systemd deployment (EC2)
- Unit file: `scripts/smart-git-proxy.service`
- Example env file: `scripts/env.example` (set `CACHE_DIR` to NVMe mount like `/mnt/git-cache`)
- Enable: `sudo systemctl enable --now smart-git-proxy`

## Configuration

All config via environment variables (or flags):

| Variable | Default | Description |
|----------|---------|-------------|
| `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `CACHE_DIR` | `/mnt/git-cache` | Directory for cached packs |
| `CACHE_SIZE_BYTES` | `200GB` | Max cache size (LRU eviction) |
| `ALLOWED_UPSTREAMS` | `github.com` | Comma-separated allowed upstream hosts |
| `UPSTREAM_TIMEOUT` | `60s` | Timeout for upstream requests |
| `AUTH_MODE` | `pass-through` | `pass-through`, `static`, or `none` |
| `STATIC_TOKEN` | - | Token for `AUTH_MODE=static` |
| `MAX_PACK_SIZE_BYTES` | `2GB` | Max allowed pack size from upstream |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `ALLOW_INSECURE_HTTP` | `false` | Allow http:// upstreams (not recommended) |

## Notes / limits
- Only smart HTTP upload-pack is handled (`info/refs?service=git-upload-pack`, `git-upload-pack` POST).
- Cache keys are request-based (URL + body hash); no repo object-graph indexing yet.
- Does **not** support `https_proxy` / CONNECT tunneling (use `url.insteadOf` instead).
