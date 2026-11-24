# Development

## Install dependencies

From the repository root on Linux:

```bash
sh scripts/setup.sh
```

Setup supports Debian/Ubuntu, Fedora/RHEL, Alpine, Arch, and openSUSE families.
Run as your normal user; native package installation uses `sudo` or `doas`.
Arch setup performs a full system upgrade with `pacman -Syu`. Enabled repositories
must provide the packages; setup does not enable repositories automatically.

Setup installs Make, Bash, Git, OpenSSH, curl, CA certificates, timezone data,
archive/checksum tools, ripgrep, ShellCheck, and a C compiler for race tests.
It keeps compatible Go (1.27.4+) and Node.js (22.18+ within v22, or 24.12+) with npm;
otherwise it installs checksum-verified Go 1.27.4 and Node.js 24.22.2 in `.tools`.
It also downloads Go modules and installs locked frontend dependencies.
Automatic downloads support Linux x86_64 (glibc/musl) and ARM64 (glibc);
ARM64 Alpine needs compatible native Node/npm packages.

Useful setup options:

- `make setup`: rerun setup after Make is installed.
- `sh scripts/setup.sh --skip-system-packages`: provision toolchains and dependencies
  when native prerequisites are already installed, without requesting privileges.
- `sh scripts/setup.sh --system-only`: install only native prerequisites; an administrator
  can run this before you rerun normal setup.

Make finds managed toolchains automatically. For direct Go/Node/npm commands:

```bash
export PATH="$PWD/.tools/go/bin:$PWD/.tools/node/bin:$PATH"
```

## Run locally

For a fresh checkout, follow the [initial key and account setup](README.md#install).
Stop any server using port 8080, then run these in separate terminals:

```bash
make dev
```

```bash
make web-dev
```

Open [http://127.0.0.1:5173](http://127.0.0.1:5173), using `127.0.0.1` rather than
`localhost`. Vite proxies API and terminal traffic to the backend on port 8080.
Development uses the existing `.env`, database, and accounts. Tests use temporary fixtures.

For a standalone development server, run `make build-dev`, then
`./bin/piped-dev --development`. See `./bin/piped-dev --help` for temporary listener,
public URL, database, and env-file overrides, and `init-admin --help` for fixture accounts.
Production and development builds use `CGO_ENABLED=0`; only race tests enable CGO.

## Commands

Run Make commands from the repository root.

| Command                | Purpose                                                                |
| ---------------------- | ---------------------------------------------------------------------- |
| `make setup`           | Install tools and project dependencies.                                |
| `make dev`             | Start the development Go backend for Vite.                             |
| `make web-dev`         | Start Vite with frontend hot reload.                                   |
| `make build`           | Build `bin/piped` with the production frontend embedded.               |
| `make build-dev`       | Build `bin/piped-dev` with development integrations.                   |
| `make frontend`        | Build production frontend assets.                                      |
| `make frontend-dev`    | Build development frontend assets.                                     |
| `make check`           | Run lint, tests, frontend/tooling checks, and Go vulnerability scans.  |
| `make lint`            | Run ShellCheck, gofmt checks, go vet, ESLint, and Prettier checks.     |
| `make format`          | Apply Go and frontend formatting.                                      |
| `make test`            | Run Go tests and race tests in both profiles, plus frontend tests.     |
| `make check-vuln`      | Scan Go source and tests in both profiles with pinned govulncheck.     |
| `make docker-binary`   | Export a standalone Linux binary to `bin/piped` using Docker.          |
| `make docker-image`    | Build and load the production scratch image as `piped:local`.          |
| `make lint-docker`     | Run Dockerfile build checks.                                           |
| `make check-setup`     | Test setup and production builds in six Linux distribution containers. |
| `make check-databases` | Test both backend profiles against the Docker database matrix.         |
| `make clean`           | Remove `bin` and `web/dist`.                                           |
| `make clean-cache`     | Remove `.cache`; run only when builds and checks are stopped.          |

Docker commands require Docker Engine with Buildx/BuildKit. Container checks clean up
their disposable resources. Setup reports go to `test-results/setup-containers.json`;
the database matrix writes `.cache/database-checks.json`.
Cleanup commands preserve `.tools`, `.env`, and application data.

`make check-vuln`, `make check`, and binary builds require network access to the Go
module proxy and vulnerability database. govulncheck is pinned in
`scripts/govulncheck.version`; scans fail on errors or findings. Builds also verify
that development integrations are excluded from production artifacts.

For frontend-only commands, use `npm --prefix web run <script>`; see
[web/package.json](web/package.json) for lint fixes, preview, and individual checks.

## Docker build and startup

Docker builds need no local Go or Node installation. Run `make docker-image`, or
`make docker-binary` for just the executable. Without Make, use:

```bash
docker buildx build --no-cache-filter checked --platform linux/amd64 --load --tag piped:local .
docker buildx build --no-cache-filter checked --platform linux/amd64 --target binary --output type=local,dest=bin .
```

Make defaults to `DOCKER_PLATFORM=linux/amd64` and `DOCKER_IMAGE=piped:local`;
use `DOCKER_PLATFORM=linux/arm64` for ARM64. The scratch image includes CA certificates
and timezone data and defaults to UID/GID `10001:10001`.

Create a private persistent directory and generate its configuration using your host UID:

```bash
mkdir -m 700 deployment
docker run --rm --user "$(id -u):$(id -g)" \
  --mount type=bind,src="$(pwd)/deployment",dst=/var/lib/piped \
  piped:local keygen
```

Edit `deployment/.env`: set `PIPED_LISTEN=0.0.0.0:8080`,
`PIPED_PUBLIC_URL=https://piped.example.internal`, and `PIPED_TRUSTED_PROXIES=`.
Place an HTTPS reverse proxy in front of `http://127.0.0.1:8080`, preserve the public
Host header, and enable WebSocket upgrades. To use forwarded client IPs, set trusted
proxies to the actual proxy peer IP or narrow CIDR seen inside the container.
Settings come from the mounted `.env`, not `--env-file` or `PIPED_*` environment variables.

```bash
docker run --rm -it --user "$(id -u):$(id -g)" \
  --mount type=bind,src="$(pwd)/deployment",dst=/var/lib/piped \
  piped:local register

docker run -d --name piped --restart unless-stopped \
  --user "$(id -u):$(id -g)" \
  --cap-drop ALL --security-opt no-new-privileges=true \
  --mount type=bind,src="$(pwd)/deployment",dst=/var/lib/piped \
  --mount type=bind,src=/etc/localtime,dst=/etc/localtime,readonly \
  -p 127.0.0.1:8080:8080 piped:local
```

The mount preserves `.env` and `data/piped.db`; keep `.env` private and back up both.
Skip key generation or registration for an existing deployment. `/etc/localtime`
uses the host timezone for cron; without it the image uses UTC.
Use `docker logs piped` for output and `docker stop piped` to stop.

## Internal development integrations

With explicit development enablement and loopback URLs, `piped-dev` exposes the
scoped bearer-key API at `/api/v1`, MCP at `/mcp`, and session-based WebMCP in supported
browsers. Discover tools before using them. These integrations are excluded from
production; use the existing local development database, never production data.

Issue only needed scopes with `piped-dev api-key --username NAME --ttl 1h --json`.
Capture its token privately in the calling process, send it only as a bearer header,
and revoke the returned ID in a cleanup/finally block with
`piped-dev api-key --revoke ID --json`. Create and remove temporary test records through
the development interfaces, rather than editing SQLite directly.
See [AGENTS.md](AGENTS.md) for access rules and [REQUIREMENTS.md](REQUIREMENTS.md) for scope
and dependency approvals.
