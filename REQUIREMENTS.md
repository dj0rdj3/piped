# Piped - current requirements

Updated: 2026-11-26 (Europe/Belgrade). This is the effective contract. Historical
requests and architectural decisions are in [docs/DECISIONS.md](docs/DECISIONS.md);
procedures are in [docs/OPERATIONS.md](docs/OPERATIONS.md). [API.md](API.md) specifies
transport payloads and [AGENTS.md](AGENTS.md) specifies working rules. Update this
contract when an agreed requirement changes; record the reason in the decision log.

## Scope and priorities

Security takes priority over simplification and convenience. Keep the service clean,
small and understandable, with meaningful regression and failure coverage. Intended
use is a private homelab, 100+ hosts, 1-10 administrators and 5-10 simultaneous jobs.
Every account is an administrator; hosts, credentials and jobs are shared. Preserve
all existing functionality and established UI behavior during architectural work.

Reassess designs against current standards and established best practices before
implementation. Choose the simplest complete solution, verify changeable guidance
against authoritative sources, and explain material tradeoffs. Existing code is
not a reason to retain an inferior design. Never take shortcuts merely to finish
sooner or compromise security, correctness, stability, or required verification.

Go backend; Vue, Tailwind utility classes and Lucide frontend; embedded production
assets. SQLite is the default; PostgreSQL, MariaDB and MySQL remain supported.
Deliver a standalone Linux binary and a non-root scratch Docker image with CA roots
and timezone data. Production and development builds use CGO_ENABLED=0. Only race
tests may enable CGO. Runtime settings come from the private .env file.

Compatibility with old internal code or application versions is not required. Preserve
supported compatibility features, including the database families, and existing data.

No new libraries without explicit approval, including test/development libraries.
Approved direct Go dependencies are modernc.org/sqlite, go-sql-driver/mysql, pgx/v5,
x/crypto, x/sys, coder/websocket and the development-only official Go MCP SDK, with
needed existing transitive dependencies. Approved frontend/tooling dependencies are
Vue, Tailwind, @lucide/vue, Vite and its Vue/Tailwind plugins, ESLint and Prettier.
ShellCheck and pinned govulncheck are approved development tools. Exact versions
are recorded in go.mod/go.sum, npm lockfiles and scripts/govulncheck.version.

Out of scope: unrelated workflows, file transfer, uploads, jump hosts, automatic key
installation, general-purpose SSH shells, new roles, log downloads and an audit trail.
Configurable SSH concurrency, script timeouts and automatic script retries remain
unapproved. Refactoring does not authorize dropping databases or existing features.

## Authentication and trust boundaries

- Local username/password login; 8-128 Unicode characters; bounded Argon2id work
  (64 MiB, three iterations, one lane). No public signup or default credentials.
- Initial account uses interactive piped register with hidden confirmation. piped
  starts the server; piped keygen exclusively creates a new private configuration.
- Sessions use 256-bit random tokens, store only SHA-256 hashes, and expire after
  eight hours. HttpOnly, SameSite=Strict cookies; HTTPS uses Secure and __Host-.
- Logout revokes its session. Password changes revoke all of the user's sessions
  and API keys. Prevent self-deletion and deletion of the last administrator.
- Browser user management and personal-key management require current-password
  confirmation. Ordinary host/job/credential/notification writes do not.
- Recheck session/key ownership, expiry, scopes and password version inside every
  privileged transaction. Keys cannot create other keys. Never trust a client actor.
- Require same-origin browser mutations and the custom request header. Keep CSP,
  anti-framing, nosniff, no-store and HTTPS/HSTS protections. HTTP is loopback-only;
  network deployment requires an HTTPS reverse proxy and a private backend listener.
- All routes accept only the configured public-origin or listener Host, including
  its port. Forwarded host headers cannot change this check.
- Resolve forwarded IPs only through configured trusted proxy peers, walking from
  the nearest trusted hop. Invalid/oversized chains fall back to the socket peer.
  Forwarding never changes origin/host/development access checks.
- Bound authentication to ten attempts per fifteen minutes per IP/account, password
  work to two concurrent derivations, limiter memory to 4096 keys, and bearer traffic
  to 120 requests/minute per IP/key. Bound JSON bodies and expensive key imports.
- API requests have a 15-second budget, with shorter bounded storage/control work.
  Storage deadlines are retryable failures; they do not invalidate valid credentials.

## Encryption, credentials and hosts

- Keep .env private (0600), regular and outside version control. The deployment key
  is 32 random bytes, base64 encoded. Verify the vault before using stored secrets.
- Use authenticated encryption bound to record ID and field/name. Keep credentials,
  passphrases, host passwords, sudo passwords and named secret values encrypted.
  Never return private keys, passwords or named secret values through any transport.
- SSH credentials support Ed25519 generation and RSA/ECDSA/Ed25519 imports, including
  protected keys. Verify public/private consistency, supported sizes and bounded KDF
  costs. Public keys may be copied; private keys cannot be exported after creation.
- Derive generated public-key comments from app username/system hostname. Preserve
  supplied import comments. Paste/file inputs must preserve forms on picker cancel.
- Hosts have names, DNS/IP addresses, SSH ports, usernames, tags, either reusable SSH
  keys or per-host passwords, and optional sudo capability/passwords. Preserve omitted
  secrets; explicitly clear sudo secrets when requested or when sudo is disabled.
- Apply optimistic versions and transactional reference checks. Assigned credentials
  and individually selected hosts cannot be deleted while referenced by definitions.

## Jobs, admission and scheduling

- Each job has one script (at most 64 KiB UTF-8), ordered individual hosts, dynamic
  case-sensitive host tags, exclusions, sequential/parallel mode and failure policy.
- Resolve targets transactionally at admission: explicit order first, tag matches in
  stable name/ID order, deduplicated, with exclusions applied before the 1-1000 limit.
  Saved empty tags remain valid for future members. Preserve all picker bulk-select,
  partial-tag, exclusion, ordering and hidden-selection semantics.
- Admit an immutable snapshot of script, connections, effective sudo policy, secret
  ciphertext and selected enabled notification configurations. Check current versions
  and authority atomically; refuse changed/deleted references.
- Copies check source version/read authority and re-encrypt secret values for the new
  job ID without returning plaintext. Omitted fields preserve supported edit semantics;
  copies default to paused scheduling and inherit reviewed notification selections.
- Named job secrets: at most 16, uppercase safe environment names of 1-64 characters,
  UTF-8 values of 1-4096 bytes, no NUL, at most 64 KiB total. Reserve runtime/startup/
  loader/command-search variables. Omit to preserve, [] to remove, omit value to retain.
- Recurring jobs use five-field cron in the server's timezone. Preserve numeric/name,
  range/list/step, day-field and DST rules and next-three-occurrence previews.
- Start scheduling at the next minute, skip missed slots and overlaps, never catch up
  or queue occurrences. Recheck current minute, version, activation/watermark and
  persisted unfinished runs at admission. Edits do not retroactively run a minute.
- jobs:write plus runs:write is required to enable or edit an enabled schedule.
  Persistent schedules do not depend on the creator's session/key remaining active.
- Require a durable request identity for every manual admission; retries retrieve
  existing history without script replay. Created and existing results are distinct;
  only an explicit uncertain-commit error authorizes reconciliation. Fresh authority and request identity/fingerprint checks remain mandatory.

## Execution, ownership and terminal history

- One server owns a database's executions. Acquire ownership before recovery; reject
  competing controllers and fence execution writes against the current generation
  and live SQL ownership session. Ownership loss stops admission and stale writes.
- Explicitly distinguish remote execution completion from durable result completion.
  Retry persistence independently per host without replaying scripts. Keep unfinished
  admissions/results tracked and prevent scheduled overlap while finalization is pending.
- Lost admission acknowledgements must reconcile the exact admission without starting
  uncertain scripts twice. Restart recovers history and preserves durable host outcomes;
  it never replays scripts. Do not claim exactly-once remote execution over SSH.
- Join workers before releasing ownership or closing storage. Cancel remote work first,
  flush masking/output with a bounded persistence drain, and keep shutdown bounded.
- Unknown/changed SSH host keys require explicit exact-fingerprint approval before
  authentication. Scheduled runs wait too. Accepting trust requires hosts:write plus
  runs:write; denial requires runs:write. Reconnect pins the approved key/endpoint.
- A job elevates only when both job and host enable sudo; root connects directly.
  Separate sudo authentication from script input, support passwordless sudo, preserve
  managed sudo argv behavior, and require no_new_privs/setpriv for non-root scripts.
- Secret injection uses bounded echo-free frames after privilege setup. Never pass
  values through command arguments, files or eval. Mask exact values and PTY CRLF forms
  across packet boundaries before persistence. Transformed/partial/exfiltrated values
  cannot be reliably masked; administrator scripts and remote accounts are trusted.
- Input targets the selected script's terminal, including Ctrl+C/Ctrl+D. Queued
  input follows caller cancellation and rechecks authority before writing; admitted
  input is delivered once with a bounded wait and is never automatically retried. Per-host
  cancellation kills its process group/children and follows stop-other-hosts policy.
  Deliberately detached processes escaping the group are outside the guarantee.
- Runs outlive HTTP requests, modal closure and navigation. Preserve durable output and
  restore selected public job/run/host IDs; never persist drafts, output or credentials
  in browser storage. Genuine failure outranks interruption (exit 130), then cancellation.
- Browser sockets require exact origin and session authorization, never bearer keys.
  Reauthorize operations and idle connections, bound them to four/session and 64 total,
  input to 4096 bytes, frames to 32 KiB and network writes to ten seconds.
- Independent control/status and output channels preserve input responsiveness. Grant
  one bounded output page at a time with monotonic acknowledgements; never retry input.
  Subscribe before replay and announce completion only after final output is delivered.
  Upgraded sockets outlive handshake deadlines; individual operations stay bounded.
- Initial history loads the latest 256 KiB; older history pages preserve byte offsets,
  split UTF-8/control sequences, scroll anchors and live appends. Virtualize terminal
  rows, retain scroll/follow state and bound temporary flood tests with watchdogs.
- Retain all output until configured expiry; purge completed history at startup/hourly.
  Compress finished-host output asynchronously into bounded indexed gzip blocks, atomically
  replacing raw rows while preserving cursors/bytes. Retry incomplete compression;
  reject corrupt blocks and keep healthy tails readable without scanning older archives.
- Reclaim SQLite space incrementally with bounded yielding steps. Startup conversion
  precedes recovery and must preserve records on failure. Server reclamation runs only
  when idle, respects scheduled/manual admission priority, uses bounded cancellation
  and safe vendor operations, and never falls back to blocking full table copies.

## Notifications and interfaces

- Reusable explicit webhook targets support GET/POST/PUT, readable URL/header templates,
  JSON/plain-text bodies, enabled/default flags, optimistic versions and named encrypted
  secrets. Destinations may be internal; verify TLS and disable redirects/ambient proxies.
- Notification names/placeholders are case-insensitive; preserve encrypted name bindings.
  Up to 16 secret values (1-4096 bytes); reject duplicate/reserved/unknown names and unsafe
  expanded URLs/headers. Origins and header names remain literal.
- Support $host/$job/$status/$output/$output:N and $$; validate JSON before/after expansion,
  escape values by context, limit templates to 64 KiB/rendered bodies to 1 MiB and output
  to 256 KiB. GET has an empty plain-text body. Select at most 64 targets per job.
- Deliver asynchronously once per host completion, with four concurrent deliveries and
  ten-second request limits. Delivery failure does not change run outcomes. No durable
  notification queue, retries or restart replay. Log sanitized diagnostics only.
- Preserve browser routes and production /api/v1 with explicit scoped personal keys.
  Keys use 256 random bits, persist hashes, reveal raw tokens once, and are owner-managed.
  Expiries are 1h/1d/7d/30d/90d/no expiration; default 30d, explicit zero means no expiry.
- Development HTTP /api/development/v1, MCP /mcp, WebMCP, key issuance and fixture cleanup
  are internal and absent from production artifacts. They require development builds,
  explicit enablement, loopback and their separate expiring scoped key namespace/purpose.
- All transports share validation/application commands and transactional authorization.
  WebMCP reads use browser sessions and writes open regular forms for user completion.
  Production and development keys cannot access each other's transports.

## UI and resource lists

Preserve routable login/protected pages, safe local return paths, default Jobs landing,
compact neutral styling, lime accents, automatic light/dark mode, mobile containment,
keyboard accessibility and guarded backdrop/Escape/unload dismissal. Keep shared table
controls/64px rows, search/filter/count/loading/error/empty states and secret form guards.
Use DD MMM [YYYY], HH:MM timestamps; omit the current client year. Execution dates are
client-local; schedule previews retain server-local calendar time and a timezone label.

All six resource tables have cancellable infinite scroll with viewport-based initial
batches, serialized stable-cursor prefetch, full-collection search and accurate counts.
Retain loaded rows on failures, allow explicit retry and ignore stale account/page results.
Move paginated queries into storage without losing authorized job-host search, tags,
exclusions, Unicode ordering, ID selections or complete unpaged integration/form reads.

## Configuration, compatibility and verification

Supported settings and defaults remain in [.env.example](.env.example), generated
configuration and runtime defaults; update all together on change. Preserve the private
.env file, all existing records, the 500 retained credentials and reusable caches.
Database migrations are checksummed and immutable once committed. New changes are
additive; reject corrupt/pending/unknown histories. Switching database families does not
migrate data automatically. Historical reset/migration-squash instructions are completed.

Use gofmt/go vet, ESLint/Prettier and ShellCheck; builds/tests do not replace linting.
Use both production/development tests and race checks, dependency/source/binary scans,
production integration-exclusion checks and common database contract tests. Exercise
real protocol faults, concurrency, shutdown, authentication, masking, sudo, compression,
pagination and UI races; prove regressions reject faulty behavior where practical.
Use authenticated development interfaces for temporary live records and revoke keys in
finally blocks; destructive checks use isolated fixtures. Monitor RAM/disk and adapt
bounded Docker/build/test concurrency to use available resources with reserve headroom.
Remove task artifacts, containers/volumes and newly pulled images; keep existing caches.
