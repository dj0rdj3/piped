# Application boundaries

The current contract is [REQUIREMENTS.md](../REQUIREMENTS.md). Historical decisions
are separate from this description of the implementation.

## Execution

`execution.Engine` owns admission, scheduling, database ownership and the active run
registry. Each admitted run has one controller and its own mutex. Host phases move
from queued/connecting/awaiting approval/running to stopped, then persisted. A stopped
script cannot resume; its immutable result can still be awaiting a storage retry.
The parent completes only after every host result is durable. Failed persistence
never invokes the SSH runner again. A bounded shutdown drain leaves unfinished
history recoverable by the next owner.

Manual admissions can carry a caller-generated request identity. Its user, job,
reviewed version and run ID are committed with the snapshot. Repeated requests
reauthorize and return public history, without obtaining connections or launching
scripts. An uncertain COMMIT is reconciled against that exact server-generated run
ID. Identities expire with retained run history; remote execution has no exactly-once
guarantee. Scheduled admissions retain their transactional minute watermark and
unfinished-run overlap check.

`lifecycle` defines request, storage, input, cleanup, ownership and shutdown budgets.
Request cancellation does not stop admitted scripts. Persistence has a separate drain
context; ownership loss cancels both execution and stale persistence authority.

## Application and transports

Host, credential, job, notification and account services own validation and secret
handling. The store rechecks authority, versions and references inside transactions.
Execution commands and read results are typed in `execution`; HTTP, terminal sockets
and development MCP call those operations. Transport code decodes input and formats
responses. Cookie/CSRF, production-key and development-key authentication stay separate.
Development integrations are excluded by build constraints and production asset checks.

## Storage

Dialect descriptors own connection invariants, parameter binding, writer locking,
durability checks, ownership queries and maintenance cancellation. SQLite retains its
private-file protections. Server transactions serialize authorization and mutations
through the existing writer lock. Schemas remain explicit vendor-specific immutable
migrations; the same behavioral tests run against every supported database family.

Paginated reads count/filter/seek in SQL and hydrate only selected public rows and
relations, under one authorized transaction. Explicit ID reads also load only the
selected records. A public metadata index stores Go-folded
search text and binary name/ID ordering, preserving Unicode semantics across database
collations. Resource writes maintain it atomically. Host changes refresh affected job
search, respecting tag selection and exclusions; host-name search is available only
with fresh hosts:read authority. Personal-key metadata is owner-filtered. No passwords,
key hashes, ciphertext, scripts or secret values enter the list index.

The first open backfills the index transactionally from existing public metadata.
Failure leaves the original records intact and retries on the next open. Complete
unpaged reads remain available for integration clients and form choices.

## Verification

Shared test-database helpers inject stalled replies and lost admission COMMITs before
or after server success. Invariant tests cover non-replay, persistence separation,
revocation, fencing, bounded contexts, masking, output history and shutdown. Pagination
regressions cover Unicode/literal search, stale cursors, relationship changes, owner
isolation, backfill and bounded row loading. `make check` adds lint, both build profiles,
race checks, frontend tests and source vulnerability scans. The Docker database runner
applies explicit CPU/memory limits and removes its disposable fixtures.
