# SSH command service — requirements and decisions

Last updated: 2026-11-26 (Europe/Belgrade)

## Status and working agreement

Product implementation is authorized as of 2026-11-23. Start with the Go server and Vue
frontend, prioritizing the database and authentication. Creating and updating this document
remains authorized.

Use this document as the reference for future planning and implementation. Keep confirmed
requirements separate from suggestions and unanswered questions. Record the user's answers
here and update the relevant requirements. An unanswered question or suggested default is
not an approved decision. Continue referring to this file when implementation is authorized.

## Product goal and design priority

Build a simple service for running commands on remote hosts over SSH, inspired by Rundeck
and Termix but with less complexity. Intended for personal use and small homelab teams.

Security is the highest priority, followed by well-written code backed by extensive,
meaningful tests. Keep the product clean, modern, simple, and easy to understand. This applies
to the user experience and implementation. Avoid adding features that obscure the core
workflow. Simplicity and convenience must not compromise security or correctness.

## Confirmed first-version requirements

### Technology and delivery

- Backend: Go.
- Frontend: Vue, Tailwind CSS, and Lucide icons.
- Always style the frontend with Tailwind utility classes. Use custom CSS only as a last
  resort when Tailwind cannot express the styling; keep shared design tokens in Tailwind's
  theme configuration rather than adding custom component classes.
- Supported databases: SQLite (default), PostgreSQL, MariaDB, and MySQL (R64).
- Deliver a standalone binary and a Docker image based on `scratch` (R68).
- Keep production runtime configuration in `.env` and minimize command-line options.
  Command-line overrides and noninteractive fixture setup belong to development builds.
- Select the database driver and connection parameters in the private `.env` file (R64).
- Draw visual inspiration from Termix and Semaphore UI. Specific layouts are not yet defined.
- Follow the system light/dark preference automatically and use a lime-green accent.
- Keep backgrounds, surfaces, borders, and ordinary text neutral white/black/gray. Reserve
  lime for the logo, primary actions, and a few small highlights.
- Keep UI copy brief and functional; omit marketing taglines and redundant explanatory text.
- Use compact page margins and spacing around navigation, tables, forms, and dialogs.
- Prevent document overflow from mobile tables and hidden labels. Render status accents as
  thin straight lines clipped by the rounded container, rather than colored curved borders.

### Library policy

- Use libraries when necessary; avoid introducing dependencies for simple tasks that can be
  implemented clearly with the standard library or existing code.
- Ask the user before introducing any new library, including backend, frontend, and test
  dependencies. Explain its purpose and why it is needed before requesting approval.
- Record approved library decisions here. The specified technology choices (Go, Vue,
  Tailwind CSS, Lucide, and SQLite) remain confirmed; additional library choices are not
  automatically approved.
- Frontend build tooling approved on 2026-11-23: Vite, `@vitejs/plugin-vue`, and
  `@tailwindcss/vite`. These compile Vue components and Tailwind styles. Exact installed
  frontend dependency versions are recorded in `web/package.json` and `web/package-lock.json`.
- Use `@lucide/vue` for the approved Lucide Vue integration; npm marks the older
  `lucide-vue-next` package as deprecated and directs users to `@lucide/vue`.
- Frontend code-quality tooling approved on 2026-11-23: `eslint`, `@eslint/js`,
  `eslint-plugin-vue`, and `prettier` as development-only dependencies. Use ESLint for
  JavaScript/Vue checks and Prettier for readable, consistent formatting.
- Shell script linting approved on 2026-11-24: ShellCheck as a development-only
  system tool, installed by setup and run through `make lint`.
- Backend libraries approved on 2026-11-23: `github.com/mattn/go-sqlite3` supplies the
  `database/sql` SQLite driver; `golang.org/x/crypto` supplies Argon2id password hashing.
  The existing `golang.org/x/sys` dependency also supports hidden interactive terminal input.
  Exact versions are in `go.mod`
  and verified module checksums are in `go.sum`.
- Database libraries approved on 2026-11-24: `modernc.org/sqlite` for pure-Go SQLite
  (R63), plus `github.com/go-sql-driver/mysql` for MariaDB/MySQL and
  `github.com/jackc/pgx/v5` for PostgreSQL, including required transitive dependencies
  (R64). All shipped builds must keep CGO disabled now and in future development.
- Development MCP support approved on 2026-11-23: the official
  `github.com/modelcontextprotocol/go-sdk` and its required transitive dependencies.
  The SDK is pinned to v1.8.0 and excluded from production builds.
- Dependency restraint must not justify inventing cryptographic primitives or other security
  mechanisms where established standard-library facilities or an approved library are needed.

### Security, code quality, and tests

- Treat security as the overriding requirement throughout design, implementation, and review,
  even though deployment is internal and all authenticated users are administrators.
- Write clear, maintainable, well-structured code with explicit error handling.
- Use appropriate linters and formatters for future code changes and fix findings in the
  changed code. Builds and tests do not substitute for linting; additions to tooling must
  follow the dependency-approval policy.
- Prioritize easily readable code: descriptive names, straightforward control flow,
  cohesive functions, and readable Vue templates rather than dense one-line markup.
- Extract components where reuse or clearer responsibilities justify them. Do not overdo
  componentization with unnecessary wrappers, tiny single-use components, or speculative
  abstractions. These are standing rules for future implementation work.
- Question whether each extraction is justified. Keep fixed values and unconditional
  styling inline when extracting them adds indirection without making the code clearer.
- Avoid literal non-ASCII characters in source where practical. Use HTML entities in
  markup, ASCII punctuation in terminal/API messages, and Unicode escapes for required
  code/test values. Continue supporting Unicode user input.
- Include extensive, meaningful automated tests, not just happy-path examples.
- Cover core behavior, authentication and sessions, credential encryption and secret handling,
  SSH host-key approval and changes, sudo behavior, and interactive terminal access.
- Test execution sequencing/parallelism, cancellation and remote process cleanup, failure
  policy, schedule overlap/missed runs, and log retention.
- Include failure cases, invalid inputs, concurrency behavior, and regression tests for bugs
  found during development. Use unit, integration, and end-to-end tests as appropriate.
- Exact tooling and coverage targets are not yet selected; any new testing libraries require
  the same approval as other dependencies.
- Proactively use MCP, WebMCP, and the internal keyed development API for future development,
  including relevant inspection, setup, mutations, debugging, and verification. Manage
  short-lived keys automatically, exercise the affected interfaces, and clean up test data.
  Destructive checks use temporary fixtures; production integration restrictions still apply.

### Intended environment and scale

- Personal use through small teams in a homelab.
- Internal network access only.
- Target 100+ managed hosts, 1–10 users, and 5–10 simultaneous jobs.
- A job can target multiple hosts. The number of simultaneous SSH sessions is not yet specified.

### Authentication and user management

- Local username/password login.
- Sign-in has its own `/login` URL. Protected-page entry redirects there while signed out,
  and successful login returns to the requested local page.
- Passwords require at least eight characters.
- Include user management.
- Label the account screen “Users” and use “user” in its controls; there is no
  team-management feature.
- Hosts, credentials, and jobs are shared across users.
- Every user is an administrator and can manage all users, hosts, credentials, and jobs.
- No role hierarchy or per-user resource restrictions are required. Stored private keys
  still cannot be viewed or downloaded, as specified below.

### Host management

- Create and manage SSH hosts.
- Store an IP address or DNS name, SSH port, and friendly name per host.
- Support tags.
- Assign a reusable SSH key credential from the credential list, or supply a host-specific
  SSH username and password.
- Password credentials belong to individual hosts and are not reusable list entries.
- Hosts configure whether sudo is available, with their optional sudo password.
- Jobs opt into sudo; only hosts with sudo enabled elevate. Root connections run directly.
- Each host supports an optional, separate sudo password secret.
- The same job script must work across hosts that require a sudo password, use passwordless
  sudo, or connect as root. Use each host's settings to handle privilege elevation and supply
  the stored sudo password when needed; root connections do not need elevation.
- No separate host grouping feature, bulk import, or SSH jump hosts in the first version.

### Credentials and secrets

- Maintain reusable SSH key credentials.
- Import SSH keys and generate new SSH keys.
- Encrypt all stored credentials/secrets at rest, including private keys, SSH passwords,
  sudo passwords, and stored key passphrases.
- A master encryption key supplied at deployment is acceptable.
- Store the deployment master key in a private `.env` file, as confirmed on 2026-11-23.
  Keep it out of source control and back it up separately from the database.
- Imported/generated private keys must not be viewable or downloadable afterward.
- Support importing passphrase-protected SSH keys.
- Generated public keys are installed manually on hosts; provide access to the public key
  for copying. Automatic public-key installation is not required initially.
- Add `app-username@system-hostname` comments to generated/derived public keys; preserve
  supplied public-key comments on import. Allow pasted public/private
  keys or loading either field from a file; cancelling a picker must preserve the form.
  A supplied public key must match the private key; derive it when omitted.
- Give frontend pages routable URLs, including `/users` and `/credentials`, and serve the
  correct SPA page when either URL is opened directly, including after login.

### SSH host verification

- Show a popup presenting the server's SSH host key with Accept and Deny actions.
- Approval is explicit, rather than silently trusting an unknown host.
- Scheduled execution waits for explicit approval when a host key is unapproved.
- A changed host key blocks execution and triggers a fresh Accept/Deny popup.

### Job authoring and execution

- Create and manage jobs.
- Select which hosts a job runs on.
- Provide a built-in script editor.
- Each job consists of one shell script.
- Configure sequential or parallel execution in each job.
- Configure per job whether a host failure stops execution on other hosts.
- Support cancellation of an individual host's execution.
- Cancellation terminates that host's remote script and its child processes.
- Cancellation follows the job's stop-other-hosts policy, just as a host failure does.
- User selected cancellation only from the proposed execution controls; configurable concurrency
  limits, execution timeouts, and automatic retries are not first-version requirements.
- Multi-step workflows, script parameters, and uploaded files are outside the initial scope.
- Privilege elevation requires both the job's Use sudo option and the host's sudo capability.
  Script sudo prefixes do not change this decision.

### Scheduling

- One-time execution means a user manually starts a job through the GUI.
- Recurring execution uses cron syntax.
- Use the timezone of the system hosting the service, not each remote SSH target.
- A separate one-off future execution feature and a visual schedule builder are not required.
- Skip a scheduled occurrence if the same job is already running; do not queue an overlapping
  scheduled run.
- Skip schedules missed during service downtime; do not catch up after restart.

### Live interaction and history

- Highest-priority execution feature: live, interactive output with a command line that lets
  the user intervene on a specific host during a job.
- Send user input directly to the running script's terminal on the selected host, including
  responses to interactive prompts and terminal controls such as Ctrl+C.
- A separate shell is not the requested intervention model. Do not reduce this requirement
  to read-only streaming logs.
- Retain all job output history/logs for a configurable number of days.
- Configure retention in `.env` with `PIPED_LOG_RETENTION_DAYS`; a missing value defaults
  to 7 days, as confirmed on 2026-11-23.
- Compress each host's complete execution output after it finishes, replacing raw storage.
  Keep live output uncompressed and decompress finished output in bounded blocks on read.
  Handle both formats transparently in the backend, without UI changes (R132).
- Downloadable logs and a separate audit trail have not been confirmed.

## Future extensibility and exclusions

- Docker image delivery is authorized in R68, superseding the earlier binary-only phase.
- R120 authorizes webhook notifications. R113 authorizes a separate production API for users; internal
  development API, MCP, and WebMCP integrations remain prohibited in production.
- The API deferral concerns an external integration API, not the communication needed between
  the Vue frontend and Go backend.
- Retain SQLite while supporting PostgreSQL, MariaDB, and MySQL (R64).
- Webhook notifications are authorized in R120. File transfer remains outside scope.
- Interactive intervention within a job is required. A standalone general-purpose SSH terminal
  outside a job has not been requested.
- Do not introduce unrelated features merely to match Rundeck, Termix, or Semaphore UI.

## Initial questions and user responses

Responses below preserve the user's answers; the sections above organize their implications.

| ID | Topic | User response |
| --- | --- | --- |
| Q01 | Audience and installation | “personal use to small team, homelab. both docker image and standalone binary.” Delivery order later revised: binary initially, Docker later (R01). |
| Q02 | Authentication and sharing | “yes. shared everything.” Local username/password login confirmed; all users later confirmed as admins in F06. |
| Q03 | Additional host features | “yes dns names and tags, no everything else.” |
| Q04 | Credential types and key handling | “passwords shouldnt be reused but per-host. all credentials should be encrypted at rest.” Key passphrases and manual public-key installation later confirmed in F03. |
| Q05 | Deployment encryption key and private-key access | “yes. no.” A deployment-supplied encryption key is acceptable; subsequent private-key viewing/downloading is not. |
| Q06 | Host-key verification | “there should be a popup showing the key and giving the accept/deny option” |
| Q07 | Job structure and sudo | “just a single shell script. each host should have a sudo password secret as well” |
| Q08 | Execution controls | “configurable in the job (sequential or parallel). only cancellation, per host. host stopping others should be configurable as well.” |
| Q09 | Manual execution and schedules | “one time means whenever manually ran through the gui. recurring through cron, timezone of the host system.” |
| Q10 | Interaction and history | “most importantly, live interactive output with a command line to intervene for a specific host. all job output history logs kept for x days (configurable).” |
| Q11 | Scale and network | “100+ hosts, 1-10 users, 5-10 simultaneous jobs. internal only.” |
| Q12 | Integrations and UI | “not initially but build with option to add webhooks and api later. use tailwind css and lucide icons, draw ui look inspiration from termix and semaphore ui.” |
| Q13 | Overall priority | “above all, keep it all clean, modern, simple and easy to understand.” |

## Follow-up questions and responses

All seven follow-up questions have been answered. Responses below record the final decisions,
including where the user chose a different approach from the suggested options.

| ID | Question | Response / decision |
| --- | --- | --- |
| F01 | Intervention model | “running scripts terminal.” Input goes directly to the selected host's running script terminal. |
| F02 | Optional sudo secret and elevation control | “yes. should have a run as sudo per host not job, so you can execute the same command on multiple hosts even if some require a password, some are passwordless, and some are root.” |
| F03 | Key passphrases and public-key installation | “yes. yes.” Support passphrase-protected keys; manual public-key installation is sufficient. |
| F04 | Unapproved and changed host keys | “wait for approval. yes, popup again.” Scheduled runs wait; changed keys block execution pending fresh approval. |
| F05 | Cancellation behavior | “yes. yes.” Terminate the remote script and children; apply the job's stop-other-hosts policy to cancellations. |
| F06 | Permissions | “everyone is an admin and can manage everything” |
| F07 | Overlapping and missed schedules | “skip. skip.” Skip scheduled occurrences while that job is running and skip schedules missed during downtime. |

## Subsequent user refinements

These later instructions take precedence over earlier requirements where they differ.

| ID | Topic | User instruction |
| --- | --- | --- |
| R01 | Delivery order | “for the beginning only binary, later docker.” |
| R02 | Library restraint and approval | “use libraries when necessary but do not over-rely on libraries to do the simplest things. ask when introducing new libraries.” |
| R03 | Highest priorities | “most importantly of everything, keep it **secure** and well written with lots of tests.” |
| R04 | Setup authorization | “you can begin setting up the basics. install the necessary libraries and tooling for working with the tech stack.” Development tooling and dependency setup are authorized; product implementation remains deferred. |
| R05 | Frontend build tooling | “Approve Vite and its Vue/Tailwind plugins.” Vite, `@vitejs/plugin-vue`, and `@tailwindcss/vite` are approved. |
| R06 | Privileged commands | “if there are any commands that need to be ran with sudo just give them to me and i will execute them.” Provide privileged installation commands for the user to execute. |
| R07 | Implementation authorization | “begin making the go server/vue frontend. database and auth first.” |
| R08 | Backend dependency approval | “Approve SQLite driver and Argon2id library.” Use `github.com/mattn/go-sqlite3` and `golang.org/x/crypto`. |
| R09 | Command-line registration and startup | Add `piped register` with interactive administrator username and hidden password input. Running `piped` with no arguments starts the server. |
| R10 | Password minimum | “drop the password requirement to 8 characters.” Apply an eight-character minimum across registration and account management. |
| R11 | UI appearance and copy | Automatic light/dark mode, a lime-green accent, and concise functional UI text; remove extra explanations and taglines. |
| R12 | UI density | “less padding around the ui.” Tighten page and component spacing. |
| R13 | Neutral palette | Use white/black/neutral backgrounds and surfaces, with only a few lime-accented elements. |
| R14 | Account UI naming | Call the account administration screen “User management”; use users rather than team/member terminology. |
| R15 | Logo and page naming | Keep the logo lime-accented and shorten the account screen label to “Users”. Supersedes R14's page name. |
| R16 | Mobile overflow and status accents | Fix unnecessary mobile scrolling; replace colored left borders with straight thin lines clipped by the corner radius. |
| R17 | Credential implementation and master-key storage | Implement the credentials page and backend with security as the priority. Store the deployment encryption key in a `.env` file. |
| R18 | Key comments, import forms, and page URLs | Public-key comments use the current app username and system hostname. Provide paste/file inputs for both keys, preserve the form when file selection is cancelled, and support direct-entry frontend routes such as `/users`. |
| R19 | Login URL and original import comments | Use a dedicated `/login` URL rather than showing sign-in at a protected page URL. Preserve imported public keys' original comments. Supersedes R18's comment behavior for imported public keys. |
| R20 | Tailwind-first styling | Always use Tailwind classes. Custom CSS is a last resort for styling that cannot be expressed through Tailwind utilities. |
| R21 | Future code quality | Use linters, write easily readable code, and componentize where useful without overdoing it. Apply these as standing rules for future code changes. |
| R22 | ASCII source and justified extraction | Use HTML entities for special characters where possible. Keep unconditional FormInput classes inline, and always question whether extracting a value, helper, or component improves the code. |
| R23 | Development integrations | Integrate MCP, WebMCP, and an API with keys for development. The official Go MCP SDK and its required dependencies are approved. |
| R24 | Production isolation | Above all, keep the integrations secure and inaccessible in production. Exclude server implementations and browser tools from production builds; require a separate development build and explicit enablement. |
| R25 | Internal agent access only | Do not provide a GUI for the development API. Only AI tooling needs it and should handle access automatically. Remove the API-key page and browser management endpoints; keep programmatic local key issuance and revocation. |
| R26 | Shared encryption-file default | Always default to `.env`, including `make dev` and development binaries. Do not select an environment-specific encryption filename. |
| R27 | Shared local environment | Remove the separate development data folder and configuration setup. Development uses the existing local `.env`, `data/piped.db`, and accounts. Keep the development-only integration build restrictions. |
| R28 | Use development integrations | Make proactive use of all relevant MCP, WebMCP, and internal development API features in future development for easier implementation and verification. Handle scoped keys and cleanup automatically. |
| R29 | Execution engine | Build the execution engine on the existing Jobs page. |
| R30 | Log retention | Configure retention in `.env`; use 7 days when no value is supplied. |
| R31 | Page revisits | Closing and revisiting Jobs restores history and input to the same running terminal. |

## Decision log

| Date | Source | Confirmed decision |
| --- | --- | --- |
| 2026-11-23 | Initial request | Gather requirements and maintain this Markdown reference; do not implement yet. |
| 2026-11-23 | Initial request | Build a simpler SSH command service inspired by Rundeck and Termix using Go, Vue, and SQLite, with future PostgreSQL/MariaDB support. |
| 2026-11-23 | Initial request | Include host management, SSH key import/generation, jobs with a script editor, manual/recurring execution, authentication, and user management. |
| 2026-11-23 | Q01–Q03 | Homelab/personal/small-team use; Docker image and standalone binary; local login and shared resources; DNS names and tags. |
| 2026-11-23 | Q04–Q06 | Per-host passwords; encrypted secrets at rest with deployment-supplied key; no subsequent private-key viewing/export; explicit SSH host-key approval popup. |
| 2026-11-23 | Q07–Q09 | Single-script jobs; per-host sudo secrets; per-job sequential/parallel mode and failure policy; per-host cancellation; manual GUI runs and cron using service-host timezone. |
| 2026-11-23 | Q10–Q11 | Interactive per-host intervention is a priority; configurable log retention in days; 100+ hosts, 1–10 users, 5–10 simultaneous jobs; internal access only. |
| 2026-11-23 | Q12–Q13 | Tailwind CSS and Lucide; Termix/Semaphore UI inspiration; future API/webhooks; prioritize clean, modern, simple, understandable design. |
| 2026-11-23 | F01–F02 | Interactive input goes to the running script's terminal; optional sudo password and Run as sudo setting per host support password-based sudo, passwordless sudo, and root connections in the same job. |
| 2026-11-23 | F03–F04 | Passphrase-protected SSH keys; manually install public keys; scheduled runs wait for host-key approval; changed keys require a fresh approval popup. |
| 2026-11-23 | F05–F07 | Cancellation terminates remote script/children and follows the stop-other-hosts policy; every user is an admin; skip overlapping scheduled runs and schedules missed during downtime. |
| 2026-11-23 | R01 | Standalone binary only initially; Docker image deferred. Supersedes the earlier first-version delivery of both formats. |
| 2026-11-23 | R02 | Keep dependencies purposeful and minimal; ask before introducing any new library. |
| 2026-11-23 | R03 | Security is the highest priority; require well-written code and extensive meaningful automated tests. |
| 2026-11-23 | R17 | Implement reusable SSH key management; store the master encryption key in a private deployment `.env` file. |
| 2026-11-23 | R04–R06 | Set up the development environment and approved dependencies. Use Vite with its official Vue/Tailwind plugins. The user will execute commands requiring sudo. |
| 2026-11-23 | R07–R08 | Begin server/frontend implementation with database and authentication first; approve the SQLite driver and Argon2id library. |
| 2026-11-23 | R09 | Provide interactive initial administrator registration via `piped register`; default to server startup when no command is supplied. |
| 2026-11-23 | R10 | Lower the password minimum to eight characters. |
| 2026-11-23 | R11 | Follow system light/dark mode, use lime accents, and remove unnecessary UI prose. |
| 2026-11-23 | R12 | Reduce spacing around the UI for a more compact layout. |
| 2026-11-23 | R13 | Remove green tints from the UI surfaces; reserve lime accents for primary actions and small highlights. |
| 2026-11-23 | R14 | Rename the account UI to User management and remove team/member terminology from its visible labels. |
| 2026-11-23 | R15 | Restore the accented logo and label the page, navigation, and breadcrumb “Users”. |
| 2026-11-23 | R16 | Contain mobile table overflow and use clipped straight status accent lines. |

## Details not yet selected

The requirements above establish the agreed feature scope. These remaining implementation
or default choices have not been approved and do not authorize additional features:

- Maximum simultaneous SSH sessions across multi-host jobs.
- Downloadable logs and a separate audit trail are unconfirmed, not first-version requirements.
- Supported deployment platforms beyond Linux, additional dependencies, and architecture
  for later features remain open implementation decisions. Linux setup support is in R96.

## Planning boundary

Server and frontend implementation are authorized, beginning with the database and
authentication. Routine implementation choices may be made within the confirmed scope.
Additional libraries still require approval before introduction. Future product features
and the open questions above remain unconfirmed.

### Initial implementation defaults

These are implementation choices, not additional user requirements:

- Use Go's standard HTTP server, `database/sql`, embedded frontend assets, and transactional,
  checksummed SQL migrations. SQLite uses WAL and foreign keys; the data directory is private.
- The undeployed baseline uses one schema file per table, including its indexes, with
  final table definitions and no follow-up alterations or data backfills (R55).
- Initialize the first administrator through `piped register`, prompting for a username,
  hidden password, and password confirmation. Keep `piped-dev init-admin --password-stdin`
  for development automation. There is no public signup endpoint or default account/password.
- Hash passwords using Argon2id (64 MiB, three iterations, one lane); allow 8–128 Unicode
  characters. Session tokens contain 256 random bits; store only SHA-256 token hashes.
- Sessions have an eight-hour absolute lifetime. Logout revokes the current session; password
  changes revoke every session for the affected account. Cookies are HttpOnly and SameSite=Strict,
  and use Secure and the `__Host-` prefix for HTTPS deployments.
- Allow plaintext HTTP only for loopback development. Network use requires an HTTPS public URL
  and a TLS reverse proxy. Trust forwarded client IPs for rate limiting only from
  configured proxy addresses, using `PIPED_TRUSTED_PROXIES` (R61).
- Limit authentication/reauthentication attempts to ten per fifteen minutes per account and
  client IP, with bounded memory for both password hashing and rate-limit state.
- Require same-origin API mutations with a custom request header and current-password
  confirmation for account changes. Prevent self-deletion and removal of the last administrator.
- Production binaries omit the MCP/development HTTP implementations and reject development
  flags and internal key-issuance commands. R113 adds a separate production API and personal
  API Keys page; internal keys remain CLI-only. Production frontend assets also omit
  WebMCP modules. Every build defaults to
  `data/piped.db` and `.env`, preserving existing local accounts across build profiles.
  Automated tests use temporary databases.
  Development integrations require explicit `--development` and accept only loopback
  listener/public URLs.
- Development API/MCP requests require expiring, scoped, revocable bearer keys with 256 bits
  of randomness. Show a raw key only at issuance and persist only its SHA-256 digest.
  Key management is CLI-only for internal AI automation, with JSON issuance output and
  local revocation. Agents capture short-lived tokens privately and revoke them after use.
  Keys cannot create other keys; password changes revoke all of the owner's API keys.
  Check stored scopes, expiry, and owner password again inside write transactions.
- WebMCP tools use the signed-in browser session, register only in development, and unregister
  on logout. Reads return public metadata; mutation tools open the normal credential form
  for human completion. User management alone requires password confirmation (R35). Never expose private-key export tools.

### Jobs management implementation (2026-11-23)

The earlier Jobs implementation deferred execution and managed saved definitions: name, shell script, ordered host targets, sequential/parallel
mode, and stop-other-hosts policy. Running scripts, interactive terminals, schedules,
and execution history were deferred at that stage. R29 below extends this implementation.
Definitions require at least one target and a script of at most 64 KiB; browser writes
use the authenticated administrator session (R35). Assigned hosts cannot be deleted until removed from jobs.
No new dependencies are required.

Job host selection uses one list. Selected rows contain their ordering controls beside
the checkbox; do not repeat selected hosts in a second list. Deselect hosts by unchecking
the checkbox, without a separate X button.


### Execution engine implementation (2026-11-23)

R29 authorizes the execution engine previously deferred after job authoring. The Jobs
page now starts manual runs and shows per-host output, terminal input and Ctrl+C/Ctrl+D,
host-key approval, cancellation, and saved run history. Runs snapshot the definition and
connections; sequential/parallel mode and the failure/cancellation policy are enforced.
Unknown or changed host keys wait for explicit approval before SSH authentication.

The current remote supervisor targets Linux with Bash, stty, sleep, and a PTY. Cancellation uses a
separate session on the same SSH connection to kill the script's process group, with
sudo where configured; terminal exit also invokes supervisor cleanup, and a terminal
watchdog removes the process group after a disconnect. Detached processes
that deliberately escape the group are not covered. SSH setup and control operations
have bounded network waits; running scripts have no configured execution timeout.
Sudo must permit execution and cleanup. Stored sudo passwords are separated from script
stdin, including with NOPASSWD rules, and terminal echo is disabled.

R30 sets `PIPED_LOG_RETENTION_DAYS` in the private `.env` file. The missing/empty default
is 7 days; accepted explicit values are 1-36500 days. Completed history is purged at startup
and hourly. Active runs are excluded. Shutdown cancels runs; restart records interrupted
runs as failed and does not replay them. The browser bounds visible output at 256 KiB;
all output remains persisted until retention expires, with cursor-based reads through the
internal tooling. The per-job history selector shows the latest 100 runs.

Development tools add separate `runs:read` and `runs:write` scopes. Job authoring authority
alone cannot execute scripts. Accepting host keys additionally requires `hosts:write`.
MCP exposes execution, status, output, terminal input, approval, and cancellation; WebMCP
adds session-authenticated execution reads. A development-only HTTP delete operation for
completed runs supports temporary fixture cleanup. Production builds exclude these
integration implementations as before. No additional dependencies were introduced.

Cron scheduling was deferred during the manual execution-engine implementation; R34 below implements it.


R31 (2026-11-23): Closing and revisiting the page must preserve run history and restore
live interaction with the same running terminal. Executions outlive browser requests.
The Jobs page remembers the selected job, run, and host per browser/account using only
public IDs, then reloads persisted output and reattaches input on return. Input drafts,
terminal contents, and credentials are not stored in browser persistence. This does not
resume an execution across a service restart.


R32 (2026-11-23): Sudo is optional per job. Use the host's sudo configuration only when
both the job and host enable it. Hosts without sudo run as their SSH user; root connections
run directly. The host form calls this capability "Allow sudo for jobs"; the job form
uses "Use sudo". New jobs default off. Migration preserves the previous behavior of
existing jobs by enabling their job flag and increments their versions to reject stale forms.
Run history snapshots the job flag, and live connections snapshot the effective decision.

Managed scripts receive a sudo compatibility function plus a private PATH helper for
child processes. Ordinary sudo prefixes run the command with the already selected identity,
including when called through command/env or nested Bash/sh. Parsing operates on argv,
not script text. Common noninteractive/stdin/environment/home flags, prompt options,
and explicit root targets are accepted; unsupported modes and other users fail clearly.
The helper never reads passwords. Real elevation and cancellation use the same effective
policy and keep the stored password separate from script input.

Non-root script processes use Linux no_new_privs through util-linux setpriv. This prevents
setuid/setgid/file-capability elevation by a real sudo binary if the helper is bypassed;
missing setpriv fails execution rather than weakening the policy. Remote targets additionally
need mktemp, chmod, rm, and /bin/bash for the private helper, which is removed by supervisor
cleanup. Absolute sudo paths are not transparently rewritten; a real sudo invocation may
fail under no_new_privs. These controls are for trusted administrator scripts, not general
isolation from arbitrary code or existing root/system privileges. No new library is introduced.


R33 (2026-11-23): Jobs support named secrets injected into script environments on each
target. Values are encrypted at rest with the deployment vault, bound to the job ID and
variable name, and never returned by browser/API/MCP/WebMCP reads. Metadata exposes only
secret_names. Authenticated browser sessions and jobs:write authorization apply (R35).
Edits support replacement, preservation, and removal, with optimistic version checks.
Runs admit an immutable ciphertext snapshot; secrets are decrypted only while a host runs.

Implementation limits are 16 secrets per job, 1-4096 UTF-8 bytes per value, without NUL,
and names of 1-64 uppercase ASCII letters/digits/underscores starting with a letter or
underscore. Shell startup, loader, command lookup, and Piped runtime variables are reserved.
The browser uses masked single-line value inputs; the API also accepts multiline values.
Omitting secrets preserves the list. A supplied list replaces it; entries without value
preserve the existing named value, and an empty list removes all secrets.

Injection happens after sudo setup and before launching the script, using bounded
base64-encoded frames on the echo-free SSH terminal. Host base64 support is required.
Values are decoded through pipes into shell variables, without files, eval, command-line
arguments, or SSH environment-request/AcceptEnv dependencies. Bootstrap handshakes keep
secret transport separate from interactive input; terminal controls/newlines in values do
not affect the wire framing. The script and its children inherit the variables.

Exact secret values and their PTY CRLF forms are masked before output persistence, across
packet boundaries, including stderr. Only possible secret prefixes are buffered, keeping
other prompts live. Encoded, modified, partial, or deliberately exfiltrated values cannot
be reliably masked. The remote account, root, and the authorized script can access its own
environment. Administrators can edit scripts, so write-only fields do not prevent an
administrator from intentionally revealing a value through another channel. No new library
is introduced.


R34 (2026-11-23): Implement cron scheduling for jobs in the Piped server's local timezone.
Skip occurrences while the same job has any unfinished run (including manual executions
and host-key approval waits); never queue or retry that occurrence. Start at the next
minute after startup and evaluate only the observed current minute, without catch-up
for downtime, delayed timers, or forward clock jumps. Enabling/changing a schedule does
not retroactively execute its current minute. Transactional admission rechecks the
current minute, job version, enabled configuration, persisted occurrence watermark,
and overlap before snapshotting a run. Execution uses the existing engine and safeguards.

The form supports enabling/pausing with a preserved five-field expression and displays
the server timezone. Job reads/API/MCP/WebMCP include schedule metadata and server timezone;
run history identifies manual versus scheduled starts and records the scheduled instant.
Supported syntax is numeric fields, wildcards, lists, ascending ranges, wildcard/range
steps, three-letter month/weekday names, and Sunday 0/7. Restricted day fields use OR;
a wildcard day field uses AND. DST missing local minutes skip, repeated local minutes
can match twice subject to overlap. No seconds, aliases, or per-job timezone prefixes.

Internal tooling must hold jobs:write plus runs:write to enable or edit an enabled job,
including edits omitting schedule. Jobs:write may disable it. Browser changes use the authenticated administrator session (R35). An enabled schedule is persistent service configuration,
not tied to the creator's login session or temporary key expiry. No new dependencies.


R35 (2026-11-23): Require password confirmation only for user management. Creating,
editing, and removing jobs, hosts, and SSH credentials use the signed-in administrator
session without resubmitting the login password. Preserve authentication, same-origin
mutation checks, scoped tooling authorization, transaction reauthorization, encryption,
and write-only secret handling. Remove the unnecessary fields from these forms and
request models. This supersedes earlier password-confirmation defaults for those resources.

R36 (2026-11-23): Run/host selections must load terminal history immediately, without
waiting for a polling timer or replaying retained history in polling-sized batches.
Fetch the latest visible 256 KiB directly on first selection, retain full output on the
server, and continue by cursor. Cancel superseded reads and reuse in-memory completed
output when switching back. Show an explicit loading state and distinguish finished
runs with no output. Format the run-history listing dates as YYYY-MM-DD HH:MM using
24-hour time in the browser's local timezone.


R37 (2026-11-23): Show executions and terminal history in a large modal rather than
an inline Jobs section. Fill the available viewport with padding on every edge and
larger horizontal margins. Let the terminal grow with the modal; keep the run selector,
host controls, terminal input, and close action accessible. Closing or pressing Escape
leaves running jobs untouched, and existing selection/history restoration remains.

R38 (2026-11-23): Order sidebar navigation as Jobs, Hosts, Credentials, Users.


R39 (2026-11-23): Double the execution modal's previous horizontal margins on larger
screens (96px per side at the small breakpoint and 160px at the large breakpoint).
Use 16px margins on all four sides on small screens. Remove the address/port from the
normal execution status row; retain endpoint identity in the SSH host-key verification
popup. On the jobs listing, show only the host name for one target; for two or more
targets show only the host count, without listing their names.


R40 (2026-11-23): Show per-host execution status only in the host tab, with its exit
code when available (including exit 0). Remove the duplicate status row. Keep host-key
review and cancellation actions available when applicable, without leaving an empty row
for completed runs.


R41 (2026-11-23): Live terminals use authenticated WebSockets for persisted output,
run/host state, and acknowledged terminal input. Keep SQLite history and cursor-based
reconnection; closing the browser must not stop jobs. The user explicitly approved
adding github.com/coder/websocket; v1.8.15 is pinned. Retain same-origin cookie/session
authorization, host-key approval, input bounds, and secret masking before persistence.

WebSocket implementation: subscribe before replay, stream only committed masked chunks,
and flush final output before completion. Wakeups are bounded/coalesced and cursor replay
is idempotent in the browser. Explicit public-origin validation and cookie sessions protect
the browser-only endpoint; development bearer-key routes remain unchanged. Reauthorize
on every input and output/state batch, plus a 10-second idle heartbeat. Bound connections
to 4 per session/64 total, input to 4096 bytes, frames to 32 KiB, and network writes to
10 seconds. Closing a connection never retries input or cancels a run. Input is acknowledged
by message ID and duplicate/non-increasing IDs are rejected. HTTP operations remain a
fallback; cancel/host-key actions keep their existing authentication and approval checks.


R42 (2026-11-23): Scrolling to the top of terminal output loads older retained history
in bounded pages over the authenticated WebSocket, including for completed runs. The
initial 256 KiB is a fast starting view, not a limit on browsable history. Preserve the
visible scroll anchor while prepending, keep live output separate while reading older
history, and offer Jump to latest. Byte offsets at chunk boundaries prevent lost or
repeated bytes, including split UTF-8/control sequences. No new dependencies.


R43 (2026-11-23): Continue appending live output to the displayed terminal while browsing
older history without moving the scroll position. Scrolling away retains the displayed
output as new chunks arrive; jumping to latest returns to the bounded tail. Put Jump to
latest in the terminal's lower-right corner as an accessible down-chevron button, shown
while away from the bottom. At the bottom, follow new output automatically. This replaces
R42's frozen historical view. No new dependencies.

R44 (2026-11-23): Remove the Load older output button and Start of output text.
Load older terminal history automatically when scrolling up, without an extra header
row. Keep the lower-right down-chevron button square with rounded corners.

R45 (2026-11-23): Remember each run/host terminal's scroll position and follow-latest
preference while switching selections in the execution modal, including cached completed
runs. Returning to a run restores its last selected host. Live updates must not prevent
scrolling up or overwrite newer user scroll input. Batch busy terminal screen updates
and render ordinary multiline output efficiently, retaining all output and immediate
input acknowledgements. Ctrl+C remains a terminal interrupt; exit 130 is reported as
Script interrupted (Ctrl+C), with its exit code, rather than a generic SSH failure log.
The reported yes run ended with exit 130 at the warning timestamp; its apparent continued
execution was queued terminal output. No new dependencies.

Render only the visible terminal rows with overscan and a sparse row index, retaining
full scrollback without creating a DOM node/string per line. Scale the scrollbar for
logs exceeding browser element-height limits. An initial unrestricted yes browser
fixture produced about 78 MB and exhausted memory through full-log layout; it was
removed, its leftover key revoked, and further flood fixtures must have a short watchdog.

R46 (2026-11-23): Show Interrupted instead of Failed for terminal Ctrl+C exits
(exit 130), for both hosts and overall run history. Keep the exit code and completion
timestamps, and preserve the job's stop-other-hosts policy. Genuine host failures take
precedence over interruption; interruption takes precedence over accompanying cancelled
hosts. Upgrade existing completed failed/130 history to the same status, without
reclassifying genuine failures, explicit cancellations, or service-restart failures.

R47 (2026-11-23): Remove the redundant Script interrupted (Ctrl+C) message below
the host tabs. Keep status and exit code in the tab; show a separate host message only
for useful failure diagnostics without an exit code. Check future UI changes for repeated
status, exit-code, and other facts already presented nearby, and avoid empty status rows.

R48 (2026-11-23): Hide the terminal input and Send/Ctrl+C/Ctrl+D controls when the
selected host has finished, letting terminal history fill the freed space. Use that
host's completion state, including when other hosts in the run are still active.

R49 (2026-11-23): Remove avoidable streaming latency, especially for terminal input
and Ctrl+C completion. Use independent authenticated WebSocket control/status and output
channels so queued logs cannot delay acknowledgements or host state. Output permits one
64 KiB cursor page in flight and requires a matching monotonic acknowledgement before the
next page; retain all backlog in SQLite. Older history stays on the output channel.
Apply state immediately and paint logs on the next animation frame, without the fixed
100 ms timer. Keep input pending separate from other actions so sending input does not
disable the whole input bar or cancellation. Do not show a stopped state before confirmed
host completion; only finish output replay after every final chunk is delivered.

Commit each bounded SSH write as one durable SQLite transaction of 4 KiB cursor chunks,
without a batching timer or weakening synchronous durability. Notify readers only after
commit; state-only subscriptions exclude log commits. Preserve origin/session checks,
reauthorization, connection/frame limits, exact input acknowledgements, secret masking,
cursor resume, and no input retries. The legacy combined socket remains compatible.
No new dependencies. Stop verification services afterward; leave the user's make dev
instance and its ports under their control.

R50 (2026-11-23): Close the execution modal when its surrounding backdrop is clicked.
Clicks inside the modal and drags starting inside it must not dismiss it. Closing the
modal leaves remote executions running, matching the existing X and Escape behavior.

R51 (2026-11-23): Do not log routine manual Ctrl+C interruptions to the server process
log; retain their interrupted status, exit code, and history in the GUI. Keep genuine
execution failures and service errors logged. Jobs is the default workspace page for
root entry, sign-in without a valid return destination, and the top-left workspace
logo. Preserve explicit safe return destinations, including Users.

R52 (2026-11-23): Use the Jobs page's icon-only refresh button consistently on Jobs,
Hosts, Credentials, and Users. Place it immediately to the left of the search field,
with the same icon size, loading animation, disabled behavior, and accessible label.
Remove the old footer refresh controls and hide empty footer rows.

R53 (2026-11-23): Match Jobs table heading typography to the other workspace tables.
Place Hosts refresh before its tag filter. Audit the UI for unintended differences
and align table rows/actions/states, search controls, page spacing, form helper text,
select sizing, saving feedback, and focus/error styling with the existing patterns.
Use the same guarded backdrop dismissal for execution and form dialogs; busy forms
remain protected and selection drags do not dismiss. Keep meaningful differences for
technical fields and the large terminal view. No new dependencies.

R54 (2026-11-23): Reset the local database and consolidate the undeployed schema into
`001_initial.sql`. Remove obsolete development upgrade/backfill migrations and their tests;
preserve the final schema, runtime behavior, and existing `.env` deployment key. Earlier
development databases are discarded rather than upgraded across this baseline reset.

R55 (2026-11-23): Clarify migration compression: keep tables in separate files, with
their final columns, defaults, constraints, and indexes defined at creation. Remove only
incremental alterations and obsolete data backfills. This supersedes R54's single-file
organization; the schema and runtime behavior remain unchanged.

R56 (2026-11-23): Minimize CLI options and keep persistent production runtime settings in
the private `.env` file. Use `PIPED_DATABASE_PATH`, `PIPED_LISTEN`, `PIPED_PUBLIC_URL`,
and the existing encryption-key/retention settings. Running `piped` requires no options;
production setup retains interactive `register` and `keygen`. Registration and development
key tooling use the configured database. Keep temporary runtime/path overrides, noninteractive
admin initialization, and task-specific API-key options only in development builds.
Generate default settings with new key files, retain compatibility with existing key-only
files, and preserve encryption keys when adding configuration entries. Explicit development
enablement and loopback restrictions remain mandatory; `.env` cannot enable the integrations.

R57 (2026-11-23): Include `.env.example` with all supported settings and their defaults.
Mark the encryption key as required with no default; never include a deployment secret.

R58 (2026-11-23): Keep `.env.example` updated in the same change whenever settings are
added, removed, or their defaults change. Record this in the project working rules and
check consistency with generated configuration and runtime defaults in `make check`.

R59 (2026-11-23): Keep `README.md` updated in the same change whenever setup or
configuration changes, including prerequisites, installation/build steps, startup
commands, settings/defaults, and development workflows. Keep examples accurate.

R60 (2026-11-24): Review all README setup instructions, commands, and configuration
examples against the implementation. Rewrite the README to be simpler, user friendly,
and focused on implemented features and getting started, with concise configuration,
operation, and development guidance rather than an exhaustive implementation reference.

R61 (2026-11-24): Configure trusted proxies in `.env` with `PIPED_TRUSTED_PROXIES`.
Default to `127.0.0.0/8` in runtime defaults, generated configuration, and `.env.example`.
Accept comma-separated IPv4/IPv6 addresses or CIDRs; an explicitly empty value disables
proxy trust. Honor `X-Forwarded-For` only from a trusted socket peer, walk right to left
to the first untrusted hop, and fall back to `X-Real-IP` only when X-Forwarded-For is
absent. Malformed or oversized forwarding headers fall back to the socket peer.
Use the resolved address for authentication and development API/MCP IP rate limits;
preserve account/key limits, the original socket peer, and host/origin/HTTPS checks.
Update README proxy setup instructions alongside configuration. No new dependencies.

R62 (2026-11-24): Fix the production sudo PTY blocker and repeat verification on
the host and nine Docker distribution environments. Sudo jobs must finish, accept
terminal input and controls, initialize encrypted job secrets, and clean up remote
children with the target's normal PTY policy enabled. Preserve password separation,
secret masking, startup-file isolation, and clean terminal output. Docker remains
verification infrastructure; delivery is still the standalone production binary.

R63 (2026-11-24): Replace mattn/go-sqlite3 with the approved modernc.org/sqlite
pure-Go driver and its required transitive dependencies. Build production and development
binaries with CGO_ENABLED=0, without musl, glibc, or a dynamic loader. Preserve WAL,
FULL synchronous durability, immediate transactions, foreign keys, busy timeouts, private
file permissions, encrypted secrets, and production isolation of development tooling.
Performance-test against the previous CGO build and verify the exact production binary
on the host and all nine Docker distributions from R62. Resetting the current database
is approved if needed; preserve the deployment encryption key. Race-detector tests may
use CGO independently of the shipped binaries.

R64 (2026-11-24): Add PostgreSQL, MariaDB, and MySQL compatibility while retaining
SQLite as the default. Configure the driver and connection parameters in `.env`.
The user approved github.com/go-sql-driver/mysql and github.com/jackc/pgx/v5,
including their required transitive dependencies. Retain modernc.org/sqlite.
Keep production and development builds CGO-free now and in future changes;
CGO remains permitted only for race-detector checks. Verify server databases
using local Docker. Delivery remains the standalone binary. Switching engines
selects a separate database; automatic cross-engine data transfer is outside scope.

R65 (2026-11-24): Extend database compatibility verification across different
major/release series, from the latest stable versions back to releases around
three years old. Use disposable local Docker databases and record exact versions
and image digests; keep SQLite regression coverage and CGO-free builds.

R66 (2026-11-24): Remove completed verification containers, volumes, and database
images after use. Keep unrelated pre-existing Docker resources intact.

R67 (2026-11-24): Clean up the growing project `.cache` directory and stale
verification leftovers. Provide `make clean-cache` for clearing project caches,
and remove temporary verification files after tasks rather than accumulating them.
Preserve application data, configuration, and reusable source checks.

R68 (2026-11-24): Provide two Docker builds: one that exports a standalone production
binary and the project's default Dockerfile build that produces an image based on
scratch. Share the production build stages, embed the frontend, and keep CGO disabled.
Include runtime CA certificates and timezone data, run as a non-root user by default,
and exclude deployment configuration, secrets, databases, and local build artifacts
from the build context. Keep configuration in the existing private .env file and
document building, persistent storage, initial setup, and reverse-proxy deployment.

R69 (2026-11-24): Add a latest-run status/time on the Jobs list, opening that run's
history; preview the next three cron matches in the server timezone; and duplicate
jobs through a prefilled creation form, including their secrets. Copy secret values
only on the server, re-encrypting for the new job ID without exposing plaintext.
Check the reviewed source version and read/write authority atomically. Copies start
with scheduling disabled in the form; explicitly enabling one requires normal
execution authority. Keep the cron field visible and editable while scheduling is
disabled, preserving and validating the expression. Show previews for paused schedules
as hypothetical times. Add unsaved-change protection to editable forms for dismissal,
page navigation, and browser unload, without persisting draft secrets. No new dependencies.
Latest-run summaries require runs:read in internal tooling; schedule previews require
jobs:read. Expose affected functionality through the existing development API, MCP,
and WebMCP read tools while keeping production integration isolation.

R70 (2026-11-24): Remove the Sudo column from the Jobs listing. Keep the job's
Use sudo option in its form and preserve execution behavior.

R71 (2026-11-24): Label the cron preview "Schedule preview" for both enabled and
paused schedules. In job host-selection rows, show only the host name; remove the
secondary address, privilege, and tags line. Preserve selection and ordering controls.

R72 (2026-11-24): Use consistent trailing divider lines on the job form's Name,
Shell script, Schedule, Secrets, Hosts, and Execution mode headings, with a clear
gap between each title and its line. Reduce the spacing below the Secrets heading.
Keep the styling in Tailwind utilities and preserve form behavior.

R73 (2026-11-24): Reserve job-form divider lines for grouped sections (Schedule,
Secrets, Hosts); remove them from single-field labels (Name, Shell script,
Execution mode). Remove the script line counter and its unnecessary layout wrapper.

R74 (2026-11-24): Remove "Workspace" from the sidebar, header, and other app copy.
The header shows only the current page title, without the redundant breadcrumb
prefix or separator. Use Piped or plain page terminology in errors, tool descriptions,
and documentation.

R75 (2026-11-24): Remove the separate app header; each page already displays its
title. Keep mobile navigation available beside the page title on every signed-in
page, including the not-found page. Keep the sidebar's close action and backdrop
dismissal, and hide the menu button when the desktop sidebar is visible.

R76 (2026-11-24): Remove the script byte counter. Keep the 64 KiB UTF-8 limit
enforced by client validation and the server, with a native textarea length bound.
Show a concise validation error only when the script exceeds the byte limit.

R77 (2026-11-24): Shorten the host sudo checkbox label to "Allow sudo"; remove
the redundant "for jobs" qualifier. Preserve sudo configuration and behavior.

R78 (2026-11-24): Add a direct Copy public key action to every credential row.
Copy the selected credential's complete public key with its comment, give success
or failure feedback, and retain the existing Public key dialog and its copy action.
Use only public metadata; private-key and secret access remains unchanged.

R79 (2026-11-24): A successful credential-row copy briefly replaces that button's
copy icon with a checkmark for two seconds instead of showing a message above the
table. Reset the feedback timer on another successful copy and clear it on unmount.
Keep accessible confirmation, failure feedback, and the existing key dialog.

R80 (2026-11-24): Apply the same two-second checkmark feedback to the copy button
inside the Public key dialog, replacing its visible success message. Preserve the
button text, accessible confirmation, clipboard failure feedback, and timer cleanup.

R81 (2026-11-24): Shorten the Public key dialog's copy button text to "Copy".
Prevent the click flicker by retaining the button's appearance and focus while
the clipboard request is pending; use its existing duplicate-click guard and
aria-disabled state instead of briefly applying native disabled-button opacity.
Keep the two-second checkmark and accessible public-key action label.

R82 (2026-11-24): Apply the same flicker fix to credential-table copy buttons:
retain their appearance and focus during clipboard writes, use aria-disabled with
the existing duplicate-click guard, and preserve the two-second checkmark feedback.

R83 (2026-11-24): Remove the authorized_keys helper text from the Public key
dialog, keeping the key, fingerprint, and copy controls.

R84 (2026-11-24): Remove the redundant visible Public key field label in its
dialog; the dialog title already identifies it. Retain an accessible field name.

R85 (2026-11-24): Give Jobs, Hosts, Credentials, and Users table data rows a
consistent 64px height, with 10px vertical cell padding and centered content.
Keep host tags on a keyboard-accessible horizontal strip so wrapping does not
make some rows taller, retaining access to all tags. Preserve empty/loading states.

R86 (2026-11-24): Remove the Users table's Access column because every account
is an administrator. Retain user names, added dates, and account actions.

R87 (2026-11-24): Remove the redundant Admin label below the signed-in user's
name in the sidebar. Keep the username, avatar, and sign-out control.

R88 (2026-11-24): Remove the trailing dot from the displayed piped app name in
the sidebar and sign-in screen. Keep the lime terminal icon.

R89 (2026-11-24): Use equal 1.2rem top and bottom padding around the sidebar logo.

R90 (2026-11-24): Use plain ASCII hyphens instead of middle-dot and typographic
dash separators throughout app copy, including browser page titles, execution
titles/history/status, credential descriptions, and user-management text. Preserve
Unicode user input and terminal output.

R91 (2026-11-24): Display execution timestamps with time before date, using
HH:MM YYYY-MM-DD in the browser's local timezone with zero-padded 24-hour time.
Apply this to run-history choices and latest-run times on the Jobs listing.
This supersedes R36's date-first order.

R92 (2026-11-24): Use the same HH:MM YYYY-MM-DD time-first format for schedule
previews. Preserve the server's calendar date, timezone, and displayed UTC offset,
including when it differs from the browser timezone or crosses a DST change.

R93 (2026-11-24): Omit the UTC offset from individual schedule preview entries,
using only HH:MM YYYY-MM-DD in server-local time. Keep the timezone shown above
the preview. This supersedes R92's per-entry displayed offset.

R94 (2026-11-24): Use DD MMM YYYY, HH:MM for displayed timestamps, omitting YYYY
when the displayed calendar year matches the client's current local year. Use English
three-letter month names, zero-padded days and 24-hour times. Apply consistently to
execution history, latest-run summaries, schedule previews, and user added timestamps.
Keep execution/account dates client-local and schedule dates server-local, with the
schedule timezone shown above the entries. Supersedes the formats in R91-R93.

R95 (2026-11-24): Keep SQLite SQL migrations in `internal/store/migrations/sqlite`,
alongside the existing PostgreSQL and MySQL/MariaDB folders. Update embedded
paths and tests without changing migration contents, versions, or checksums.
Retain separate driver-specific migrations; do not introduce an abstract schema.

R96 (2026-11-24): Unify source setup for Debian, Ubuntu, Fedora, Alpine, and other
popular Linux distributions. Use one POSIX shell entry point with native apt-get,
dnf/yum, apk, pacman, and zypper package installation, root/sudo/doas access, and
an option to skip system packages. Keep the former Debian entry point as a wrapper.
Check toolchain minimums against go.mod and package.json before installing project
dependencies; report outdated distribution toolchains clearly. Keep CGO disabled
and update README setup instructions. The user approved ShellCheck as a development
tool for setup linting in make lint. No application library dependencies are added.

R97 (2026-11-24): Remove the separate Debian setup script and keep all setup logic
in scripts/setup.sh, including its version checks. Test real setup execution in
Debian, Ubuntu, Fedora, Alpine, Arch, and openSUSE containers; record exact images,
versions, and results, and remove test containers and newly pulled images afterward.
This supersedes R96's compatibility wrapper.

Verification details: make check-setup exercises real native package installation,
locked npm installation, frontend tooling compilation, Go module downloads, and
ShellCheck in disposable containers. It records native tool versions separately
from runs that need the supported toolchains pinned in Dockerfile. The default
openSUSE fixture is stable Leap. Keep the additional Tumbleweed diagnostic report
when its current image/repositories have a package conflict; do not automatically
force downgrades, remove security policy packages, or ignore package dependencies.
Arch package installation uses pacman -Syu so fresh package databases and installed
packages remain consistent. openSUSE uses timezone and openssh-clients. Accept
Fedora's stable Go release version suffix for enabled build experiments.

R98 (2026-11-24): Make setup install required Go and Node versions automatically
so Debian, Ubuntu, Fedora, Alpine, Arch, and openSUSE need no manually supplied
toolchains. Keep compatible installed tools; otherwise install pinned Go/Node
releases with official, pinned SHA-256-verified downloads in the project's ignored
.tools directory. Keep all installer logic in scripts/setup.sh. Make must select
managed toolchains automatically; preserve global toolchains, shell profiles,
deployment configuration, and application data. Test actual full setup and repeat
setup in all six distribution containers without any harness-supplied toolchains,
including Alpine's musl Node fallback. Keep CGO disabled and document the workflow.
This supersedes R96/R97's manual toolchain prerequisite and verification-only
toolchain provisioning. Go, Node, and their required system prerequisites are
already authorized; no new application libraries are introduced.

R99 (2026-11-24): Test make build in all six distribution containers after setup,
using only scripts/setup.sh to install tools. Extend make check-setup with clean
application-source snapshots that exclude deployment data, existing toolchains,
dependency/build caches, node_modules, and prebuilt assets. Run the real production
build, its production asset/binary checks, and a CLI smoke check in each container.
Verify CGO_ENABLED=0 and absence of an ELF interpreter/dynamic library dependencies;
record image/source/binary fingerprints and results. Keep verification isolated,
preserve user data, and remove disposable containers, images, copied sources, and
compiled verification binaries after use. No new dependencies are needed.

R100 (2026-11-24): Simplify code where readability improves without sacrificing
functionality or security, and organize related code into folders, with particular
attention to backend complexity. Consolidate shared HTTP route definitions,
transaction authorization setup, and account validation/hashing. Separate backend
initialization, sessions, accounts, responses, and middleware by responsibility.
Group frontend features and their tests together, keep shared UI/helpers separate,
and consolidate repeated page-loading lifecycle and stale-response handling.
Preserve security checks, development-only integration isolation, configuration,
and dependencies.

R101 (2026-11-24): Review backend logic more deeply and simplify control flow and
state while keeping every feature and security requirement. Remove redundant
execution lifecycle state and result synchronization, merge job-copy/edit secret
preservation through one transactional path, and share cron day matching rules.
Development API, MCP, WebMCP, and key-issuance implementations must remain absent
from production artifacts, with binary and endpoint checks for that boundary.
Do not add dependencies or weaken authorization, encryption, host-key approval,
cancellation, output masking, durability, scheduling, or database compatibility.

R102 (2026-11-24): Extend the simplification review across every page and feature,
starting with SSH execution and terminal handling. Continue frontend work while
prioritizing backend changes. Make justified logic simplifications rather than
moving code for its own sake; retain necessary security and lifecycle checks.
Perform extensive regression testing across both build profiles, race checks,
SSH/terminal edge cases, page/form behavior, supported database families, and
production artifact/runtime isolation. Development API, MCP, WebMCP, and key
issuance must remain absent from production artifacts. Preserve existing data,
configuration, features, and dependencies; clean up verification fixtures.

R103 (2026-11-24): Subdivide the HTTP server directory by responsibility. Keep
terminal streaming, development-only API/MCP transports, shared HTTP responses,
and endpoint integration tests in distinct folders. Preserve the shared resource
route table, browser session authentication, scoped key authentication, rate limits,
and production exclusion checks. Judge application simplification separately from
added regression tests and verification tooling; avoid forwarding-only abstractions.

R104 (2026-11-24): Continue simplifying backend runtime logic while preserving
all features and security. Consolidate terminal streaming lifecycle and run-wide
cancellation, and remove redundant host-reference queries where transactional
foreign keys already enforce integrity. Preserve protocol compatibility, output
credit, input/status responsiveness, cancellation isolation, atomic rollback,
fresh authorization, and public errors. Verify both profiles, race behavior,
supported database families, live development interfaces, and production exclusion
of development API, MCP, WebMCP, and key issuance. Do not reorganize files for this pass.

R105 (2026-11-24): Simplify database and authentication logic without removing
features or weakening security. Share migration validation/application while
preserving driver-specific schemas, locking, transaction and interrupted-DDL
behavior; retain migration files and checksums. Share bounded password derivation,
preserve existing hash compatibility, make local development-key issuance atomic,
and avoid loading password hashes for metadata lists. Extensively test upgrades,
corrupt history, concurrency, sessions/revocation, both build profiles, supported
databases, live development interfaces, and production integration exclusion.

R106 (2026-11-24): The user approved pinned govulncheck as a development-only
tool, including its required tool dependencies. Add source scans with tests for
both profiles to make check and binary scans to production/development builds,
including the Docker production builder. Pin the version outside application
go.mod, retain CGO-free builds, document network requirements, fail on scanner
errors/findings, and keep existing API/MCP/WebMCP production exclusion checks.

R107 (2026-11-24): Review job and scheduling logic more deeply and simplify
redundant reads, writes, and matching work while retaining every feature and
security check. Preserve transactional version/authorization checks, write-only
encrypted secrets and copy semantics, schedule activation/occurrence watermarks,
overlap and missed-slot skipping, server timezone and DST behavior, and production
exclusion of development API/MCP/WebMCP. Perform extensive regression testing in
both profiles and across supported database families; preserve existing data.

R108 (2026-11-24): Simplify host and credential management logic while retaining
all features and security. Remove redundant reference queries only where enforced
database constraints and existing transaction locks preserve integrity. Preserve
public errors, stale-version handling, authentication switching, per-host secret
preservation/clearing, credential imports/generation, key validation/work limits,
encryption binding, and metadata-only reads. Extensively test both build profiles,
supported database families, concurrency, live development interfaces, and
production exclusion of development API, MCP, WebMCP, and key issuance.

R109 (2026-11-24): Simplify HTTP routing and middleware logic while preserving
all routes, feature availability, cookie/bearer isolation, authorization, CSRF and
origin/host checks, request bounds, security headers, proxy trust, and rate limits.
Avoid repeated request-invariant work, unnecessary parsing, and duplicate proxy
header passes; retain the 4096-key bound and existing limiter expiry/budgets.
Keep development API/MCP/WebMCP and key issuance absent from production. Test both
profiles, middleware edge cases and races, supported databases, live interfaces,
and production artifacts/endpoints. Preserve existing data and configuration;
normalize accepted URL scheme casing consistently for security decisions.

R110 (2026-11-24): Simplify configuration, startup and shutdown logic while
preserving settings, defaults, private-file/key validation, setup commands and
production isolation. Share lifecycle cancellation across HTTP, cleanup and
execution; retain bounded graceful HTTP shutdown, joined background work, remote
process cancellation and database-close ordering. Keep invalid settings and bind
failures from reaching run recovery. Keep development API/MCP/WebMCP and key
issuance absent from production. Extensively test lifecycle failures, signals,
configuration boundaries, both profiles, database families and live interfaces;
preserve existing deployment files and data.

R111 (2026-11-25): Massively shorten and simplify README.md to a short description,
a few key features, and a simple installation guide. Keep development dependency
installation and available commands in a separate DEV.md, linked from the README.
Keep both documents accurate when their setup or workflow instructions change.

R112 (2026-11-25): Remove shared-use and team-oriented wording from the README;
these do not need specific emphasis in the description or features.

R113 (2026-11-25): Provide a production API that users can use and an API Keys page
where signed-in users create and revoke their own personal keys. Keep the development
API separate and always exclude its transports, MCP, WebMCP, fixture cleanup, and
internal CLI key issuance from production. This supersedes the previous external-API
deferral and prohibition on a user-facing key page, only for production personal keys.
Internal development key management remains CLI-only and hidden from the GUI.

Implementation: Production bearer routes use `/api/v1`; development bearer routes use
`/api/development/v1` only with the development build, explicit enablement, and loopback
URLs. Each accepts only its own token namespace and persisted key purpose. Keep shared
resource handlers, validation, transactional authorization, scopes, and secret protection.
Personal key management uses same-origin browser sessions and current-password
confirmation, restricts listing/revocation to the owner, and cannot be performed using
bearer keys. Personal keys contain 256 random bits, persist only SHA-256 digests, expose
the raw token only at creation, require explicit scopes and expiry (1 hour to 90 days;
page default 30 days), and are revoked on password changes/account deletion. Preserve
existing keys as development keys through additive driver-specific migrations. No new
application dependencies or settings are required. Document the API and verify both
profiles, purpose isolation, revocation/expiry/scopes, browser interactions, and
production artifact exclusion of internal tooling.


R114 (2026-11-25): Allow personal production API keys without expiration. Show exactly
these expiry presets: 1h, 1d, 7d, 30d, 90d, and No expiration (the requested label for
forever keys). Retain the 30d default. An explicit zero expires_in_hours selects no
expiration; omitted/null values remain invalid. Store non-expiring production keys
with SQL NULL expiry and expose expires_at as zero metadata. Preserve existing keys,
finite expiry boundaries, revocation, password/account invalidation, scopes, and
production/development isolation. Internal development keys must still expire, and
internal tooling continues using short-lived keys. No new dependencies or settings.


R115 (2026-11-25): Remove the explanation above the API Keys table and make its table
header uniform with the established pages, including the count, refresh/search controls,
column typography, padding, and borders. Keep table presentation and states consistent.
UI uniformity is a standing default: use existing page, table, form, and dialog patterns,
avoid redundant explanatory copy, and introduce variations only when the task needs them.

The Vite development proxy matches `/api/` requests so `/api-keys` remains a frontend
page route on direct entry.


R116 (2026-11-25): Match API key revocation to the other pages' destructive row
actions: use the shared icon button's danger styling, the standard right-aligned action
container, the existing 17px trash icon, and a concise Revoke tooltip. Preserve the
accessible key-specific label and existing revocation confirmation/password flow.


R117 (2026-11-25): Review and align the entire API Keys page and its creation,
one-time display, and revocation dialogs with existing UI patterns. Include the
backend in the cleanup and simplify only where readability improves. Preserve all
functionality, password confirmation, owner-only management, explicit scopes and
expiry choices, one-time token handling, unsaved-change guards, stale-response
protection, transactional authorization, and production/development separation.


R118 (2026-11-25): Apply a focused consistency and simplification pass to the remaining
pages, dialogs, and their backend paths. Keep the review brief in light of the recent
deep reviews. Fix clear UI inconsistencies and small readability/lifecycle issues;
preserve security, functionality, existing data/configuration/dependencies, and
production exclusion of all internal development tooling. Use focused verification
for changed paths rather than repeating the full database/distribution matrices.


R119 (2026-11-25): Do not clean caches on every run or at the end of every task.
Preserve build and dependency caches for reuse; clean them only occasionally when
needed (stale/corrupt/excessively large caches) or explicitly requested. Continue
removing temporary verification files and test records after use. This supersedes
any interpretation of R67 as routine per-task cache clearing.


R120 (2026-11-25): Add notifications, starting with reusable webhook targets inspired
by Proxmox. Configure URL, POST/PUT/GET method, headers, body format/template, enabled
state, and an "On by default for new jobs" flag on each target. Each job selects its
notification targets; enabled default targets are preselected for new jobs, while edits
preserve selections and duplicates copy source selections. Existing jobs do not gain
new targets automatically. Select up to 64 targets per job; this limit also applies
to server-selected defaults and copies. An explicit empty selection disables notifications.

Templates support $host (remote host name), $job (job name), $status (per-host terminal
status), $output, and $output:10 (last ten lines, with counts 1-1000); $$ emits a literal
dollar. Notify once for each host completion, including success, failure, interruption,
and cancellation, for manual and scheduled runs. JSON variables belong inside strings
and are escaped automatically; plain-text bodies substitute literally. Only persisted,
secret-masked output is sent; output is bounded to the latest 256 KiB and rendered
bodies to 1 MiB. Body templates are limited to 64 KiB. GET requires an empty plain-text
body. No new libraries or environment settings are introduced.

Encrypt URL and header settings with record-bound vault encryption and keep them
write-only. Omission on edit preserves both; replacement supplies the complete URL
and header map. Validate HTTP(S), headers, templates, and versions. Allow internal
homelab destinations, verify HTTPS certificates, disable redirects and ambient proxy
settings, limit request duration to ten seconds and concurrent delivery to four.
Run admission snapshots selected enabled target configurations; subsequent edits
cannot change that run. Delivery happens asynchronously after persisted host completion
and does not alter execution outcomes. Log sanitized delivery failures; no retries,
durable delivery queue, restart replay, or notification history are included. Shutdown
cancels outbound requests and joins workers.

Expose target CRUD through browser routes, production/development APIs, and internal
MCP with separate notifications:read/write scopes. Internal WebMCP supports metadata
reads. Preserve production exclusion of all internal integrations and key issuance.


R121 (2026-11-25): Make Notifications' Add target action a dropdown with Webhook
as the currently available option. Leave room for future notification types. Keep
the creation form specific to its selected type and show target type in the list.
Persist an immutable type discriminator, defaulting existing targets and omitted
API creation types to webhook. Reject unsupported types rather than treating them
as webhooks. Keep existing webhook configuration, encryption, job defaults, and
delivery behavior. No new dependencies or environment settings are needed.


R122 (2026-11-25): Remove the Webhook connection heading and its encryption helper.
Make webhook URL and headers readable, editable, and unencrypted. This supersedes
R120's encryption/write-only requirement for these two configuration fields. Add
named notification secrets whose values remain encrypted and write-only, with
$secret referencing a secret named secret in the body, URL path/query, or header
values. Preserve $host/$job/$status/$output body variables and $$ literal dollars.
Keep the destination origin and header names literal. Escape substituted secrets
for their URL component and validate rendered headers to prevent injection.

Use the existing vault with secret values bound to target ID and name. Reads expose
URL/header templates and secret_names, never secret values or expanded templates.
Support up to 16 secrets with case-sensitive ASCII identifier names (1-64 characters,
starting with a letter/underscore) and UTF-8 values of 1-4096 bytes without NUL.
Reserve host/job/status/output. Omitted secrets preserve the list; supplied arrays
replace it, missing values preserve existing names, and [] removes everything.
Undefined references or invalid rendered settings reject the entire transaction.
Snapshot secret ciphertext with run notification definitions. Migrate existing
encrypted endpoints atomically at vault-backed service startup, store their URL and
headers as readable templates, clear old ciphertext, and escape existing dollar
literals so delivery stays the same. Keep scopes, authorization/version checks,
redirect/TLS protections, sanitized logging, and production isolation. Reuse the
named-secret editor for job and webhook forms without changing job behavior.
No new dependencies or environment settings are introduced.


R123 (2026-11-25): Make notification template names and secret names fully
case-insensitive: built-ins, $output:N, body/URL/header secret lookup, reserved-name
checks, duplicate checks, and preservation/replacement on edit. Preserve literal
text, secret values, and existing encrypted name bindings. Keep stored spelling
when preserving ciphertext using a differently cased name. Reject existing names
that become ambiguous or collide with built-ins before delivery; let users resolve
them explicitly without choosing or dropping values automatically. This supersedes
R122's case-sensitive notification names. SSH job environment variable rules remain
unchanged. No new dependencies, schema changes, or environment settings are needed.


R124 (2026-11-25): Clean up and simplify frontend and backend code where useful
without sacrificing security or functionality. Consolidate duplicated webhook
rendering and endpoint validation, clarify transactional secret preservation,
and simplify notification form state. Preserve authorization, atomic versioned
writes, encrypted name bindings, case-insensitive templates, validation limits,
startup conversion, delivery protections, and form lifecycle/dirty-state behavior.
No new dependencies, schema changes, environment settings, or user-visible
configuration changes are needed.


R125 (2026-11-25): Review notification frontend/backend behavior and security and
fix discovered issues. Validate JSON body templates before expansion so variables
cannot alter JSON structure or complete escape sequences. Enforce the rendered
body limit over substitutions and literal text, including trailing text. Reject
unsafe/oversized delivery before making an outbound request. Reject explicit URL
ports outside 1-65535 rather than saving unusable destinations. Preserve existing
notification security and behavior; use regression tests and live interface checks.


R126 (2026-11-25): Squash migrations to one file per application table for SQLite,
PostgreSQL, and MariaDB/MySQL, with final columns, defaults, constraints, and indexes
defined at creation. Remove incremental DDL and obsolete incremental upgrade tests.
Preserve the existing local database: recognize only the complete, exact checksummed
pre-squash schema and adopt the new baseline history atomically without application
data changes. Partial older histories must finish upgrading with the previous
binary; corrupt, pending, unknown, and incomplete histories remain rejected. Keep
driver-specific locking, transactional/pending-DDL safety, and CGO-free builds.
No dependencies or environment settings are added.


R127 (2026-11-25): As a standing rule, keep README additions extremely short,
limited to actual user-facing features. Do not describe migration organization as
a feature and do not add or relocate lengthy explanations to README.md or DEV.md.
This supersedes standing README update rules when they would require lengthy
explanations. No README/DEV entry is needed for this migration squash.


R128 (2026-11-25): Remove old migration compatibility rather than preserving
pre-squash development databases. Delete baseline-adoption code and fixtures,
legacy encrypted-webhook endpoint conversion, and its obsolete storage column.
Retain one file per table, normal checksum/sequence validation, pending-DDL safety,
and current notification behavior/security. Reset the local development database
when needed; keep .env byte-for-byte unchanged. This supersedes R126's baseline
adoption and R122/R124's legacy conversion requirements. No README/DEV additions.


R129 (2026-11-25): Remove compatibility arising from earlier development versions
project-wide, while retaining compatibility serving current functional purposes
(supported databases, protocols, SSH key formats, sudo behavior, configuration
options/defaults, and security validation). Remove the obsolete navigator WebMCP
adapter and unregister fallback, the superseded vault-only configuration helper,
and implicit notification-type backfilling for old clients. Notification writes
now require an explicit supported type. Retain normal optional update semantics.
Keep .env unchanged and reset the local development database for the clean schema.


R130 (2026-11-25): Review compatibility-removal changes and fix regressions without
restoring development-version adapters. Include notification secrets in missing
vault-verification checks so startup cannot silently initialize a new verifier
when they are the only encrypted data. Ignore late authorization failures from
stopped/cancelled WebMCP calls, and prevent session checks for an earlier account
from signing out a newly authenticated account. Retain current authorization,
registration cleanup, settings, and strict migration checks. Preserve .env and
local records; use isolated fixtures for corruption/race regression tests.


R131 (2026-11-25): Make a quick (at most 2-3 minutes) cleanup pass over recent
frontend/backend changes, preserving security and functionality. Use the WebMCP
registration abort signal as its single lifecycle state, simplify iteration over
pending migrations, and keep vault-secret checks readable. Use focused checks;
no configuration, schema, dependency, or user-data changes.


R132 (2026-11-25): Compress the whole execution log for each host when its job
finishes there, replacing the uncompressed data. Decompress on demand in chunks
when reading output. The follow-up explicitly requires backend-only behavior,
with no UI changes or storage-format indicators. Keep live output uncompressed
and distinguish raw and archived records internally. Use indexed, independent gzip
blocks with bounded memory and atomic replacement; preserve original cursors, byte
boundaries, live streaming, history pages, and webhook output. Run compression in
the background without delaying subsequent hosts or run completion. Retry incomplete
compression during startup/hourly maintenance, including recovered executions,
without changing execution outcomes. Preserve retention and authorization. Use the
standard library, with no new dependencies or environment settings.


R133 (2026-11-25): Review compression and decompression for correctness, security,
and simplification without removing functionality. Bound both database query branches
before merging output, limit archive prefetch and compressed input size, reject trailing
gzip members, and report corrupt/truncated archives as errors rather than end of output.
Keep healthy tails readable without inflating older blocks just to detect another page.
Use one background compression worker with coalesced completion wakeups; continue past
damaged hosts during maintenance. Preserve atomic replacement, cursors, authorization,
masking, retention, and UI behavior. Test real SQLite page/file/WAL usage with compressible
and incompressible output, freed-page reuse, and compaction only in isolated fixtures.
Report occupied space separately from allocated file size: compression releases reusable
pages but does not automatically shrink SQLite files. Do not vacuum or reset the local
development database. No new dependencies, schema changes, or settings.


R134 (2026-11-25): Automatically return freed SQLite disk space in the background
without pausing running jobs. Use incremental auto-vacuum, small bounded steps, a
separate fail-fast maintenance connection, and yielding/retries around busy writers
and pinned readers. Keep foreground WAL/FULL durability and authorization unchanged.
Checkpoint safely so the database file shrinks and truncate an unpinned WAL without
waiting for active transactions. Use a tracked worker with cancellation and joined
shutdown. Verify actual file allocation decreases while another host continues
persisting output, alongside busy-reader/writer, conversion, and integrity tests.

New SQLite databases enable incremental vacuum before creating tables. Existing
files need one automatic, record-preserving VACUUM conversion at server startup,
before this engine admits jobs; never run a full VACUUM during job execution or
from CLI-only key/account commands. This startup conversion supersedes R133's
restriction on vacuuming the existing development database. Preserve records,
secrets, output cursors, private permissions, .env, other database backends, and
the unchanged UI. No dependencies, environment settings, or schema migrations.


R135 (2026-11-25): Keep the SQLite reclamation review under three minutes. Pin
the startup mode change and VACUUM to one fail-fast maintenance connection; prepare
reclamation before run recovery so a failed conversion cannot change run status.
Close maintenance resources if recovery fails and recheck foreground activity before
WAL truncation. Preserve bounded steps, durability, data, settings, and dependencies;
use focused lint, conversion/locking, race, and integration checks.


R136 (2026-11-25): Run full checks and verify failed SQLite startup conversion
leaves all existing table contents, runs, host statuses, sessions, output cursors,
and database format unchanged. Force a read-only maintenance-connection failure
in an isolated fixture and test through actual engine startup. Recheck automatic
reclamation while another host persists logs. Include PostgreSQL and MariaDB
startup/logging/output checks; SQLite maintenance remains inactive on them. Keep
this verification under five minutes and preserve local application data/settings.


R137 (2026-11-25): Verify actual PostgreSQL and MariaDB database disk allocation
before compression, after compression, and after reclaiming deleted-row space,
within five minutes. Use disposable disk-backed databases and both build profiles.
Compare live payload sizes with filesystem allocation; account for PostgreSQL
TOAST/index storage and MariaDB file-per-table allocation. Check ordinary vacuum,
reuse-only vacuum, full rebuild, and InnoDB purge/OPTIMIZE behavior in fixtures.
Verify byte-exact output after cleanup. Keep potentially blocking rebuilds out of
production and preserve user databases, configuration, and dependencies.


R138 (2026-11-25): Automatically reclaim PostgreSQL/MariaDB log-file space in
the background, within a five-minute implementation/check window. To protect
running jobs from table rebuild/metadata locks, defer physical reclamation until
this engine has no active jobs, recheck persisted active runs, and retry deferred
maintenance each minute. New admissions may briefly wait for the bounded idle
maintenance operation. PostgreSQL uses ordinary VACUUM with SKIP_LOCKED and short
lock/statement limits, never VACUUM FULL. MariaDB uses NOWAIT, INPLACE, LOCK=NONE
rebuilds only for file-per-table logs with at least 1 MiB reusable space; never
fall back to a blocking copy algorithm. Discard maintenance sessions so timeout
settings cannot leak into execution queries. Preserve data, cursors, SQLite/MySQL
behavior, credentials, and configuration. Test actual disk reclamation after idle
and deferral during active runs on disposable databases in both profiles.


R139 (2026-11-25): Review server reclamation under three minutes, including locks
and admission. Cancel maintenance before waiting for the admission mutex at shutdown;
limit maintenance pool acquisition and apply fail-fast session settings before table
reads. Require no due/unprocessed schedule and a gap before the next cron occurrence
of at least twice the maintenance estimate, using the three-second operation budget
as a conservative floor and retaining higher observed durations. Keep eligibility
checks under the manual/scheduled admission lock and fail closed on schedule errors.
Manual future starts cannot be predicted and retain bounded serialization. Avoid
server maintenance/schedule scans on SQLite/MySQL. Use targeted lint/race tests.


R140 (2026-11-25): Run full project checks and the complete PostgreSQL/MariaDB
internal-test matrix in both build profiles, under five minutes. Verify the exact
2x scheduling-window boundary, shorter gaps, larger estimates, and due pending
schedules, plus cancellation before the admission lock is released and joined
shutdown. Use disposable databases, preserve user data/configuration, and clean
all verification resources afterward.

R141 (2026-11-25): Manual job starts should immediately request safe maintenance
cancellation and take priority over new maintenance. Check fresh run-write authority
before allowing interruption and revalidate it transactionally at admission. For
PostgreSQL and MariaDB, cancel only the dedicated maintenance session's statement
and wait for its server response/DDL cleanup before releasing the admission lock.
Join cancellation before discarding that session so it cannot target a pooled job
connection. Keep cancellation attempts bounded; if cancellation is unavailable,
retain the existing maintenance timeout and safe admission barrier. Preserve the
2x scheduled-job window and shutdown cancellation. No UI, configuration, or new
dependencies. Allow up to ten minutes for implementation and verification.

R142 (2026-11-25): Investigate and fix intermittent SQLite reclamation failures
within eight minutes, rather than accepting a successful rerun. Add a deterministic
regression for WAL growth after vacuum/checkpoint completion and measure physical
database plus WAL allocation while another host continues logging. Preserve
fail-fast separate maintenance connections, bounded steps, active-writer progress,
byte-exact archived output, and cancellation/startup safeguards. Simplify stale
maintenance state where possible and repeat both normal and race verification.
The investigation also found that a fixed 16x whole-file shrink assertion ignores
newly retained live log rows. Verify an empty freelist and at least 75% reduction
in combined allocated database/WAL space instead, followed by sustained logging
and byte-exact replay. Confirm the final regression rejects the old stale-state
implementation; do not hide production reclamation failures with test reruns.

R143 (2026-11-25): Run full project checks and the PostgreSQL/MariaDB internal-test
matrix in both build profiles, including manual-start cancellation, admission and
shutdown safety, SQLite reclamation regressions, and physical disk-reclamation
checks. Keep verification under ten minutes, use disposable fixtures and database
volumes, preserve deployment data/configuration and caches, and remove temporary
verification tools, harness copies, binaries, and logs afterward.

R144 (2026-11-25): Review final reclamation/cancellation, locks and admission under
three minutes. Keep pending scheduler batches ineligible for maintenance between
advancing the schedule watermark and finishing lookup/admission. Protect pending
batch accounting with the existing admission mutex and clear it on every exit.
Preserve manual priority, joined server cancellation, SQLite fail-fast reclamation,
2x windows and shutdown behavior; verify focused regression/race checks.

R145 (2026-11-25): Run the complete project checks and PostgreSQL/MariaDB matrix
in both build profiles after the pending scheduler admission fix, under ten minutes.
Include pending schedule batches, manual and server cancellation, shutdown, 2x
windows, SQLite reclamation and physical server disk reclamation regressions.
Capture individual server test results, use disposable disk-backed fixtures, preserve
deployment data and caches, and clean up all temporary verification resources.

R146 (2026-11-25): Review pending scheduler admission, locking, cancellation and
error cleanup under three minutes. Preserve the existing mutex-protected counter
and deferred cleanup unless a concrete defect requires changes. Exercise real batch
registration through normal completion and lookup errors, verify maintenance stays
deferred while an error is reported without holding admission locks, and verify
duplicate/cancelled entries cannot clear another batch's registration. Run targeted
lint/race checks; avoid unnecessary production abstractions or dependencies.

R147 (2026-11-25): Add Select all to the job's host picker, for all hosts or the
current tag/search matches. Provide an explicit tag filter. Preserve selected hosts
outside the filter and their execution order, append new matches without duplicates,
and disable bulk selection when there are no matches or all matches are selected.
Keep individual selection, ordering controls, unsaved-change protection, and normal
job validation and saving. No dependencies or backend/API changes.

R148 (2026-11-25): Change Select all to Deselect all when every matching host is
selected. Deselect only the current tag/search matches, retaining hidden selections
and their order. Keep the button disabled only when there are no matches. This
supersedes R147's disabled state when all matching hosts are already selected.

R149 (2026-11-25): Jobs may target saved host tags alongside individual host IDs,
including tag-only jobs. Resolve any matching tag afresh for every manual/scheduled
run inside the admission transaction; snapshot connections and history as before.
Preserve explicit-host order, append additional tag matches in name/ID order, and
deduplicate overlaps. Tag changes and new matching hosts affect only future runs.
Allow saved tags with no current matches; reject runs resolving to zero or over 1000
hosts with a clear error. Retain exact, case-sensitive host-tag matching. Expose
host_tags in browser forms, copies, listings, and production/development API and
MCP/WebMCP metadata. Add non-destructive table migrations for supported databases;
preserve deployment data, scopes, encryption, scheduling, and execution safeguards.

R150 (2026-11-25): Clearly identify hosts included through selected job tags in
the host picker. Mark tag-derived inclusion and show its matching tags while
preserving independent individual selection, ordering, filtering, and saved targets.
No backend, dependencies, or configuration changes.

R151 (2026-11-25): Show a regular checkmark for hosts included through job tags,
using the existing inclusion label to explain the source. Keep individual host
selection independent, with tag-included hosts remaining checked after removing
their individual selection.

R152 (2026-11-25): Bulk host selection counts individual and tag-derived inclusion.
Select all adds only currently excluded matches, preserving dynamic tags. Deselect
all removes individual matches and contributing tag selections. Preserve currently
included hosts outside the filters, selecting them individually if their remaining
tags no longer include them; retain unrelated tags and existing individual order.
Keep empty-filter operations inert. No backend or configuration changes.

R153 (2026-11-25): Unchecking a host must exclude it even when selected tags
include it. Use the same tag-removal and other-host preservation behavior as bulk
deselection, retaining unrelated tags and individual order. This supersedes R151's
requirement to keep a tag-included host checked after removing its individual
selection. No backend, dependency, or configuration changes.

R154 (2026-11-25): Persist per-job excluded_host_ids. Unchecking individual or
filtered hosts excludes them without clearing selected tags or converting other
tag members to individual targets. Rechecking/Select all clears only matching
exclusions. Future tag members remain included; exclusions take precedence across
overlapping tags and are applied before run-size checks for manual/scheduled runs.
Preserve snapshots, individual order, and hidden selections. Show excluded state
in the picker and use effective targets in listings. Include exclusions in forms,
copies, APIs, MCP/WebMCP metadata, validation, and non-destructive migrations on all
supported databases. Limit exclusions to 1000 existing distinct hosts and reject
overlap with individual selections. No new dependencies or environment settings.
This supersedes R152/R153's contributing-tag removal behavior.

R155 (2026-11-25): With no host tag or search filter, Deselect all clears individual
hosts, selected tags (including tags without current matches), and exclusions.
With either filter active, retain tag selections and deselect only matching hosts
using exclusions, preserving hidden selections. No backend or configuration changes.

R156 (2026-11-25): When unchecking the last included member of a selected tag,
deselect that tag instead of disabling the host checkbox or excluding every member.
Apply this to individual and filtered bulk deselection, including overlapping tags.
Remove exclusions tied only to the deselected tags, retaining exclusions needed by
remaining tags, unrelated tags (including empty future tags), and individual order.
No backend, dependency, or configuration changes.

R157 (2026-11-25): Selecting a job target tag clears exclusions for all its current
hosts, including hosts hidden by picker filters or overlapping another selected
tag. Preserve exclusions outside that tag and existing individual order. Loading
saved jobs and deselecting tags must not clear exclusions. No backend, dependency,
or configuration changes.

R158 (2026-11-25): Show a native partial-selection mark on selected target tags
when current members are excluded, considering all hosts regardless of picker
filters. Clicking a partial tag selects all its members by clearing their
exclusions; clicking a fully selected tag deselects it. Keep ordinary checkmarks
on included host rows. No backend, dependency, or configuration changes.

R159 (2026-11-25): Show the effective total as "Hosts (N selected)" in the job
picker: individual and tag-matched hosts combined, deduplicated and excluding
excluded hosts. Count all selections regardless of picker filters and update as
selections change. No backend, dependency, or configuration changes.

R160 (2026-11-25): Combine tag and individual host selection into one bordered,
scrollable list, with a small "tag" or "host" pill beside each name. Remove separate
tag/host sections and their helper text. Search and tag filters apply to both row
types. Preserve host totals, partial tags, exclusions, future tag membership, host
ordering, bulk-selection behavior, and saved empty tags. No backend, dependency,
or configuration changes.

R161 (2026-11-25): Remove the tag filter dropdown from the unified job host picker
and remove its unused state. Use the shared search bar as the only filter, with
Select all/Deselect all beside it. Keep name/address/tag searching, matching tag
rows, total counts, exclusions, and full deselection when search is empty.
No backend, dependency, or configuration changes.

R162 (2026-11-25): Simplify recent frontend and backend changes without losing
functionality or weakening security. Share effective job target resolution across
frontend views and consolidate transactional string-list reads. Preserve selection
semantics, validation, authorization, encryption, ordering, snapshots, and scheduling.
No new dependencies, configuration settings, or schema changes.


R163 (2026-11-25): All six resource-page tables use infinite scrolling, with an
initial batch sized to the available viewport plus a scrolling buffer. Fetch more
before reaching the bottom, serialize fast-scroll requests, cancel superseded
filter/refresh reads, and search/filter the whole collection. Preserve full-list
reads for form choices and integrations, full counts, job summaries and execution
restoration. Show a spinner while requests are pending, including slow servers;
show a retryable error on unavailable/timed-out requests without discarding loaded
rows. Verify 500 of each resource, rapid scrolling, viewport changes, failures, and
cleanup through authenticated development interfaces. No new dependencies/settings.


R164 (2026-11-25): Keep table loading indicators out of view during normal scrolling
against a responsive server. Increase the initial buffer to one viewport and begin
opportunistic prefetch on the first downward scroll movement, maintaining at least
three viewports ahead rather than waiting near the last loaded row. Retain the
furthest scroll target during pending requests so fast scrolling refills the buffer
without duplicate requests. Keep visible loading/error feedback when the server is
slow or unavailable, cancellation, filtering, and bounded page sizes.


R165 (2026-11-25): Simplify the recent frontend/backend pagination and prefetch
implementation while preserving behavior and security. Use cancellation as the
single request-lifecycle authority where possible, simplify authorized job-host
search, and retain stable cursors, scope checks, viewport buffers, errors/retries,
and execution restoration. Preserve the 500 credentials requested for personal
testing, existing records/settings, and dependencies. Verify relevant regressions
and the authenticated development interfaces.

R166 (2026-11-25): Review frontend and backend simplification within fifteen
minutes, with extensive regression testing. Reduce repeated job-list metadata
reads and host-picker work while preserving ordering, dynamic tags, exclusions,
authorization, secret non-disclosure, and all existing features. Keep production
user API and internal development API/MCP/WebMCP isolated. Preserve ongoing
uncommitted work, deployment data, configuration, dependencies, and caches.

R167 (2026-11-25): Fix all four production-readiness findings: bound and cancel
execution database operations, acquire exclusive server ownership before run
recovery, retry failed completion persistence without replaying scripts, and
report output-storage failures as failures rather than user cancellations.
Retain ownership until joined cleanup, stop admission if its server connection
is lost, and bound shutdown finalization. Preserve committed host outcomes when
recovering a missing parent completion. Add failure/concurrency regressions and
verify both profiles and supported databases. Preserve deployment data, secrets,
configuration, existing safeguards, and CGO-free builds; no new dependencies.

R168 (2026-11-26): Fix the four follow-up readiness findings and add regressions
for the exact failures. Reconcile ambiguous admission commits as failed without
executing or replaying their scripts, including scheduled admissions. Fence
execution transactions against both the current ownership generation and its
live SQL session; bound predecessor cleanup before replacement recovery. Retry
each completed host independently while other scripts continue. Preserve masked
output tails during cancellation and bounded engine shutdown. Use additive
ownership migrations, preserve existing data/configuration/dependencies, and
verify real SQL session termination and lost successful COMMIT replies against
disposable server databases, alongside race and production-isolation checks.

R169 (2026-11-26): Repeat the complete production-readiness review, emphasizing
security and stability; fix confirmed findings and recheck until no actionable
findings remain within the reviewed scope. Preserve deployment data, secrets,
existing safeguards and uncommitted work. Bound API authentication and storage
requests, startup/CLI storage operations, and periodic cleanup/compression and
notification-output reads. Keep upgraded terminal sessions long-lived with bounded
individual operations. Report storage deadlines as retryable failures without
invalidating valid sessions or bearer keys. Add regressions including targeted
PostgreSQL network stalls while ownership remains healthy; verify both profiles,
race behavior, supported databases and production tooling isolation. No new
dependencies or configuration settings.
