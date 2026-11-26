# Operations

Use the [current requirements](../REQUIREMENTS.md) for guarantees, [DEV.md](../DEV.md)
for builds/Docker commands and [API.md](../API.md) for integration contracts.

## Deploy and start

1. Build with make build or the production Docker target; CGO stays disabled. Production
   artifacts must pass the integration-exclusion and binary vulnerability checks.
2. Use a persistent private working directory. Run piped keygen only for a new deployment,
   piped register only before the first account, then piped with no runtime flags.
3. Keep .env a regular 0600 file and SQLite's directory 0700. Retain the same working
   directory and encryption key across restarts and upgrades. Do not print the key/DSN.
4. Network access requires an HTTPS reverse proxy, its exact PIPED_PUBLIC_URL, preserved
   Host and WebSocket forwarding. Bind/publish the backend privately; trust only actual
   proxy peer addresses. See DEV.md for the non-root Docker bind-mount procedure.
5. /api/health reports HTTP availability. /api/ready reports execution readiness
   without issuing a database query per probe; it uses monitored ownership and pending
   persistence state. Check startup logs for successful initialization;
   a competing execution owner, damaged migrations or wrong vault key must fail startup.
   Direct backend probes must send the configured public-origin or listener Host,
   including its port; forwarded host headers do not satisfy this check.

## Back up and restore

Back up the database and separately back up its matching private .env encryption key.
Keep both backups access-controlled. Losing the key makes encrypted values unrecoverable.

For a simple SQLite backup, stop Piped cleanly, ensure no process is using the database,
then copy the data directory including any remaining WAL files. Do not copy just the
main file while the database is running. For server databases, use the vendor's consistent
backup/snapshot procedure with matching schema/history and preserve the deployment key.

To restore, stop the service, retain the failed installation for diagnosis, restore a
consistent backup plus its matching configuration/key and private ownership/permissions,
and start the matching application version. Verify login, public metadata and a harmless
controlled SSH job. Never initialize a fresh key over an existing deployment.

## Upgrade and recover

- Take a verified backup first. Stop the old service before starting its replacement.
  Keep schema history and checksums intact; never edit them to bypass a startup error.
- Use additive migrations supplied by the new binary. A binary predating the restored
  schema is not a supported rollback; restore its matching database backup as well.
- Normal shutdown stops admission, cancels scripts, joins remote cleanup and drains
  output/results for a bounded period before releasing ownership. Interrupted history
  is recovered on restart; scripts are never replayed automatically.
- Unclean ownership takeover may wait for predecessor cleanup. A second live server
  cannot take over the same database. Investigate ownership loss/network errors before
  restarting repeatedly; do not remove a lock or history row manually.
- For an uncertain API mutation, read current resource state before retrying. Use the
  same supported admission identity when retrying an execution request. Never blindly
  replay terminal input or a remote script after losing its acknowledgement.

## Diagnose failures

- Use /api/v1/runtime with runs:read for aggregate active scripts, pending results,
  persistence retries, ownership loss and shutdown phase. This returns no identities
  or connection details. Readiness returns 503 during a persistence retry or shutdown.
- Check disk capacity, RAM, process/container limits and database availability first.
  Preserve the private configuration and records; use disposable fixtures for faults.
- API deadlines return retryable errors without invalidating valid sessions/keys.
  Verify connectivity before replacing credentials. Respect Retry-After and versions.
- SSH requires approved host keys and the documented remote tools. Verify fingerprints
  through a trusted channel. Unknown/changed keys wait before authentication.
- Sudo needs both host and job authorization. Verify target PTY/sudo policy and cleanup
  privileges. Stored authentication stays separate from terminal input.
- Output storage failures fail executions; persistence retries do not rerun scripts.
  Review sanitized service logs and durable per-host history. Webhook delivery is best
  effort and does not determine execution status.
- Compression releases reusable pages; physical reclamation is asynchronous and may
  defer while jobs/readers/writers are active. Do not run a blocking rebuild on live data.

## Development and verification

Internal API/MCP/WebMCP are for the local development build and existing development
.env/data only. Use short-lived least-scope CLI keys captured privately; revoke their
IDs in finally blocks. Create/delete fixture records through authenticated interfaces,
disable fixture notifications and never use unrelated hosts for execution tests.

Run make check with bounded Go package/test parallelism and SQL containers with explicit
memory/CPU limits. Adapt concurrency to observed headroom and recheck resources between phases.
Preserve build/module caches unless they are damaged or genuinely too large. Remove
only verification files, containers, volumes and images created by the check. Never use
system-wide Docker pruning or delete deployment data to make a test pass.

Shutdown dependency order: stop admission and maintenance, cancel SSH sessions,
join remote cleanup and final masking/output, drain result persistence within its
budget, join notifications and background workers, release execution ownership,
then close storage. Result retries never invoke the SSH session factory.
