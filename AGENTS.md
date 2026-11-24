# Project working rules

Read `REQUIREMENTS.md` before making changes. It records the agreed scope, security
requirements, dependency approvals, and subsequent user decisions.

## Code quality

- Keep production and development builds CGO-free (`CGO_ENABLED=0`), including all
  future database drivers and dependencies. CGO is permitted only for race tests.

- Update `README.md` in the same change whenever setup or configuration changes,
  including prerequisites, installation/build steps, startup commands, settings,
  defaults, and development workflows. Keep instructions and examples accurate.
- Update `.env.example` in the same change whenever supported settings are added,
  removed, or given different defaults. Keep it aligned with generated configuration
  and the README; required secrets must have empty placeholders, never real values.
- Use appropriate linters and formatters when writing or changing code, and resolve
  findings in the changed code before considering the work complete. Use `gofmt` and
  `go vet` for Go, ESLint for JavaScript/Vue, and Prettier for frontend formatting.
  Builds and tests do not replace linting. If suitable tooling is missing, address
  that gap under the dependency-approval policy in `REQUIREMENTS.md`.
- Write code for readability: use descriptive names, straightforward control flow,
  cohesive functions, and explicit error handling. Avoid compressed statements and
  dense one-line templates. Format Vue markup with readable indentation and place
  long attributes and controls on separate lines.
- Extract components when they provide meaningful reuse or make a substantial UI
  section easier to understand. Keep their responsibilities and interfaces clear.
  Avoid unnecessary wrappers, tiny single-use components, and speculative abstractions.
- Question every extraction: keep fixed values and styling inline when a separate
  constant, helper, or component adds indirection without improving understanding.
  Do not extract unconditional class strings solely to apply them unchanged.
- Keep source text ASCII where practical. Use HTML entities for special characters
  in markup (for example, `&ndash;`, `&mdash;`, and `&hellip;`), plain ASCII punctuation
  in terminal/API messages, and Unicode escapes when a non-ASCII value is needed
  in code or test fixtures. Preserve support for Unicode user input.
- Use Tailwind utility classes for frontend styling. Custom CSS is a last resort
  for styling Tailwind cannot express; shared design tokens belong in its theme.
- Run checks appropriate to the change. `make check` runs the existing project
  checks, including `make lint`. Use `make format` to apply formatting. Do not
  introduce new dependencies without the required approval.
- Remove temporary verification scripts, copied source trees, binaries, screenshots,
  and logs when a task finishes. Keep reusable checks in `scripts` and only needed
  final reports in `test-results`; do not accumulate task leftovers in `.cache`.
  Use `make clean-cache` to clear project caches when no builds or checks are running.

## Internal development access

- Proactively use MCP, WebMCP, and the keyed development API throughout future development
  when they help with inspection, setup, debugging, or verification. Use their full relevant
  functionality, including mutations needed by the task, rather than limiting them to reads.
- Discover the available MCP tools and browser WebMCP tools before using them. Prefer MCP
  for structured application operations, WebMCP for supported browser navigation and forms,
  and `/api/v1` for direct HTTP checks or operations not available through a connected tool.
  If a tool connection is unavailable, use the authenticated local endpoint or app controls.
- Create and manage test records through these interfaces instead of editing SQLite directly,
  so application validation, authorization, and encryption are exercised. Use temporary
  fixtures for destructive checks and clean up records created for testing.
- Verify affected MCP, WebMCP, or API behavior when changing features they expose, alongside
  the appropriate lint and automated checks. Use browser controls when verifying rendering
  or interactions requires them. Do not invoke unrelated mutations just to exercise a tool.
- The development API and MCP integrations are internal AI tooling. Do not add a GUI,
  navigation item, or browser endpoint for managing development API keys.
- Use the development build and loopback URLs with the app's existing `.env` and
  `data/piped.db`. Automated tests use temporary fixtures. Never enable or ship these
  integrations in production or point them at production data.
- Manage access automatically through `piped-dev api-key --username NAME --ttl 1h --json`,
  adding only the scopes needed for the task. Use the account associated with the database
  for that task.
  Capture the JSON in the calling process; keep its token out of chat, logs, command-line
  arguments, browser code/storage, and source control. Send it only as a bearer header.
- Revoke the returned key ID with `piped-dev api-key --revoke ID --json` in a cleanup/finally
  block. Do not make the user create or copy development API keys through the application.
