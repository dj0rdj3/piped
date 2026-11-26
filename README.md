# Piped

Run shell scripts on your servers over SSH from a web interface.

## Features

- Run jobs across hosts sequentially or in parallel, manually or on a cron schedule.
- Select or deselect job hosts individually or in bulk with search.
- Target tags with individual exclusions, automatically including future tag members.
- Follow live terminal output, answer prompts, and cancel individual hosts.
- Manage SSH keys, encrypted passwords and job secrets, and host-key approvals.
- Send webhook notifications with body templates and per-job target selection.
- Keep run history and compressed logs in SQLite, PostgreSQL, MariaDB, or MySQL.
- Automate through the [production API](API.md) with scoped personal keys.

## Install

From the repository root on Linux:

```bash
sh scripts/setup.sh
make build
```

Setup installs the required tools and dependencies. Already have a binary? Skip the build.
For Docker builds, development, and cache maintenance, see [DEV.md](DEV.md).

Create the encryption key and first administrator, then start Piped:

```bash
./bin/piped keygen
./bin/piped register
./bin/piped
```

Skip `keygen` if `.env` already exists, and `register` if an account already exists.
Open [http://127.0.0.1:8080](http://127.0.0.1:8080) and sign in.
Always start from the same directory: settings live in `.env`, and SQLite defaults to
`data/piped.db`. Follow the [operations guide](docs/OPERATIONS.md) for backups and recovery.

Edit `.env` to change settings; [.env.example](.env.example) lists the defaults.
For access from other machines, use an HTTPS reverse proxy, set `PIPED_PUBLIC_URL`
to its exact origin, and enable WebSocket forwarding. Keep the backend listener private.

Remote hosts need Linux, SSH with PTY support, Bash, `stty`, `sleep`, `mktemp`, `chmod`,
and `rm`; non-root jobs also need `setpriv`, and job secrets need `base64`.
