# Production API

Use `/api/v1` on your normal Piped origin. The production API is available in both
builds and requires a personal API key. HTTPS is required for network deployments;
plain HTTP is allowed only on loopback. Cross-origin browser requests are rejected.

## Keys and authentication

Sign in and open **API Keys**. Choose a name, scopes, expiry, and confirm your current
password. Copy the generated `piped_key_...` token immediately; it is shown only once.
The presets are **1h**, **1d**, **7d**, **30d**, **90d**, and **No expiration**.
The default remains 30d. Keys with No expiration have no time limit and still follow
normal revocation on password changes or account deletion. Their metadata uses `expires_at: 0`.
The session-authenticated creation request uses `expires_in_hours: 0` for No expiration;
the field must be supplied explicitly. Positive lifetimes remain limited to 1-2160 hours.
Only the owner can list and revoke their keys. Revocation takes effect on subsequent
authorization checks. Password changes and account deletion revoke all owned keys.

Send the token only in `Authorization: Bearer <key>`, never in a URL. Session cookies
cannot authenticate this API, and bearer keys cannot create, list, or revoke keys.
Use a secret manager or private environment variable for your client:

```bash
curl --fail-with-body \
  -H "Authorization: Bearer $PIPED_API_KEY" \
  https://piped.example.internal/api/v1/jobs
```

Avoid logging request headers or using verbose HTTP tracing with a real key.
Only SHA-256 token digests are stored. Key lists contain metadata, never token values.
Internal `piped_dev_...` keys are rejected by this API, including in development builds.

## Scopes

| Scope | Permission |
| --- | --- |
| `users:read` / `users:write` | List / create, delete users, and change passwords. |
| `credentials:read` / `credentials:write` | List public SSH credential metadata / generate, import, rename, delete credentials. |
| `hosts:read` / `hosts:write` | List / create, update, delete hosts. |
| `jobs:read` / `jobs:write` | List definitions and preview cron / create, update, delete jobs. |
| `notifications:read` / `notifications:write` | List webhook target metadata / create, update, delete targets. |
| `runs:read` / `runs:write` | Read history and output / execute, send input, cancel, and approve host keys. |

Write scopes do not imply read scopes. Enabling or editing an enabled schedule also
requires `runs:write`; approving a host key also requires `hosts:write`. Job copying
requires `jobs:read` as well as `jobs:write`. Latest-run summaries require `runs:read`.
`users:write` grants administrator account changes; select it only when needed.

## Requests and responses

Send `Content-Type: application/json` for request bodies. No browser CSRF header or
current-password field is needed for bearer operations. JSON fields not in the
operation's schema are rejected. Collection reads return an object with an array:
`users`, `credentials`, `hosts`, `jobs`, or `runs`. `GET /me` returns `user`.
Create/update operations return the resource under `user`, `credential`, `host`, or
`job`; rename, delete, password, and terminal-control operations return HTTP 204.
Starting a run returns `run` with HTTP 201.

Account, key, resource, and run timestamps are Unix seconds; schedule-preview
`occurrences` are RFC 3339 strings with offsets and a separate `server_timezone`. Resource IDs are strings except numeric user IDs.
Host/job edits and deletes require the current numeric `version`; stale versions
return 409. Delete requests for these resources use a JSON body with `version`.
Passwords, private keys, passphrases, and job secret values are never returned.

## Endpoints

Paths below are relative to `/api/v1`. `{id}` is the resource ID. Run operations use
`{run}` and `{host}` from run metadata.

| Method | Path | Required scope |
| --- | --- | --- |
| GET | `/me` | Any valid personal key |
| GET / POST | `/users` | `users:read` / `users:write` |
| DELETE | `/users/{id}` | `users:write` |
| POST | `/users/{id}/password` | `users:write` |
| GET / POST | `/credentials` | `credentials:read` / `credentials:write` |
| PATCH / DELETE | `/credentials/{id}` | `credentials:write` |
| GET / POST | `/hosts` | `hosts:read` / `hosts:write` |
| PUT / DELETE | `/hosts/{id}` | `hosts:write` |
| GET / POST | `/notifications` | `notifications:read` / `notifications:write` |
| PUT / DELETE | `/notifications/{id}` | `notifications:write` |
| GET / POST | `/jobs` | `jobs:read` / `jobs:write` |
| PUT / DELETE | `/jobs/{id}` | `jobs:write` |
| GET | `/jobs/schedule-preview?cron=...` | `jobs:read` |
| POST | `/jobs/{id}/run` | `runs:write` |
| GET | `/jobs/{id}/runs` | `runs:read` |
| GET | `/runs/{run}` | `runs:read` |
| GET | `/runs/{run}/hosts/{host}/output?after=0` | `runs:read` |
| POST | `/runs/{run}/hosts/{host}/input` | `runs:write` |
| POST | `/runs/{run}/hosts/{host}/cancel` | `runs:write` |
| POST | `/runs/{run}/hosts/{host}/host-key` | `runs:write` + `hosts:write` |

Key management and WebSocket terminals use browser sessions and are not exposed here.
Deleting saved execution history is an internal development operation, unavailable
through the production API.

### Request examples

Create a user: `{"username":"automation","password":"a-long-unique-password"}`.
Change a user's password: `{"password":"another-long-unique-password"}`.
You cannot delete your own account or the last administrator.

Generate an SSH credential: `{"name":"Deploy","mode":"generate"}`.
Import instead with `mode: "import"`, `private_key`, optional `public_key`, and
optional `passphrase`. Rename with `{"name":"New name"}`. Credential responses
contain public metadata only.

Create a password-authenticated host:

```json
{
  "name": "Example host",
  "address": "server.example.internal",
  "port": 22,
  "username": "deploy",
  "password": "replace-with-a-secret",
  "run_as_sudo": false,
  "tags": ["linux"]
}
```

For key authentication, supply `credential_id` instead of `password`. On updates,
omitting a password preserves it; an empty `sudo_password` clears that secret.
Disabling sudo removes its password, and selecting a credential removes the host's
SSH password. Use PUT with the full editable settings and current `version`.

Create a job:

```json
{
  "name": "Check uptime",
  "script": "uptime",
  "host_ids": ["HOST_ID"],
  "execution_mode": "sequential",
  "stop_on_failure": true,
  "run_as_sudo": false,
  "schedule": {"enabled": false, "cron": "0 2 * * *"},
  "secrets": []
}
```

`execution_mode` accepts `sequential` or `parallel`. Scripts are limited to 64 KiB
UTF-8. Cron uses five fields in the server timezone. Omit `schedule` or `secrets`
on updates to preserve them. A supplied secrets array replaces the list; entries
have `name` and optional `value`, where an omitted value preserves that named
secret. An empty array removes all secrets. Copy using `copy_from: {"id":"SOURCE_ID",
"version":1}` in a creation request; secret copying happens on the server.

Run a job with POST `/jobs/{id}/run` and `{"version":1}`, using its current version. Host-key approval uses
`{"fingerprint":"SHA256:...","accept":true}` with the pending fingerprint from
run metadata; deny with `accept:false`. Send terminal input with `{"data":"text\n"}`.
Cancel an individual host with POST `/runs/{run}/hosts/{host}/cancel` (no body required).
Output returns `chunks` with `id` and base64-encoded `data`; pass the last chunk's `id` as `after` for the next read.
Use `tail=1` without `after` to fetch the bounded recent tail and a `truncated` flag.

## Errors and limits

Errors are JSON: `{"error":"message"}`. Typical statuses: 400 invalid input,
401 missing/expired/revoked key, 403 missing scope or rejected origin/host,
404 missing resource, 409 conflicting version/state, 415 wrong content type,
429 rate limit, and 503 busy authentication work. JSON bodies have endpoint-specific
limits and an overall 1 MiB bearer request limit.

Bearer requests are limited to 120 per minute per client IP and per key. HTTP 429
includes `Retry-After: 60`. Authentication attempts for browser key management retain
the separate login/password-confirmation limits. All API responses use `Cache-Control:
no-store`. Keys, ownership, expiry, and scopes are rechecked for authorized operations,
including inside database write transactions.

The internal development API is a separate transport at `/api/development/v1`, uses
CLI-issued development keys, and is compiled out of production along with MCP,
WebMCP, and fixture-cleanup handlers. See [DEV.md](DEV.md).


### Webhook notification targets

`GET /notifications` returns `targets`; POST/PUT return `target`. Create with:

```json
{
  "type": "webhook",
  "name": "Chat",
  "method": "POST",
  "content_type": "application/json",
  "body": "{\"text\":\"$job on $host: $status\\n$output:10\"}",
  "enabled": true,
  "default_enabled": true,
  "endpoint": {
    "url": "https://chat.example.internal/webhook/$token",
    "headers": {"Authorization": "Bearer $token"}
  },
  "secrets": [{"name": "token", "value": "replace-with-secret"}]
}
```

Targets require an immutable `type` discriminator, currently `webhook`.
Unsupported or omitted types are rejected.

URL and header templates are stored unencrypted and returned under `endpoint`.
PUT requires the current `version`; omit `endpoint` to preserve it, or replace the
whole URL/header map. `secret_names` lists the named secrets; their encrypted values
are never returned. Keep private values in `secrets` and reference a secret named
`token` as `$token` in the body, URL path or query, or header values. Substitution is
case-insensitive and runs once; `$$` emits a literal dollar. URL secret values are
escaped for their path/query component and cannot change the URL origin. Header
names are literal, and expanded values must pass HTTP header validation.

Omit `secrets` on edit to preserve the list. A supplied array replaces it: entries
with `value` create or replace a secret; entries without `value` preserve an existing
name. An explicit `[]` removes all secrets. Undefined references reject the entire
update, including removal of a referenced secret. Use up to 16 secrets, each with a
1-64 character name (`[A-Za-z_][A-Za-z0-9_]*`) and a 1-4096 byte UTF-8 value without
null characters. `host`, `job`, `status`, and `output` are reserved in every case.
Secret names differing only in case are duplicates. Built-ins also ignore case:
`$HOST`, `$Job`, and `$OUTPUT:10` behave like their lowercase spellings. Edits can
preserve an existing secret using any casing without changing its value. Existing
stored spellings remain valid; pre-existing names conflicting with a built-in or
each other must be renamed/replaced or removed before that target can send again.

DELETE takes `{"version": 1}`; assigned targets must be removed from jobs first.

`method` accepts POST, PUT, or GET. `content_type` accepts `application/json` or
`text/plain`; GET requires an empty plain-text body. Transport headers and
Content-Type cannot be overridden. HTTPS certificates are verified, redirects
and ambient proxies are disabled, and HTTP(S) internal homelab URLs are supported.
Requests time out after ten seconds, with four concurrent deliveries.

JSON body templates must be valid JSON before expansion, with all variables inside
strings and outside escape sequences. Rendered bodies, including literal text, are
limited to 1 MiB; invalid or oversized bodies are never sent.

Job requests/responses include `notification_target_ids` (at most 64). On creation, omission
selects enabled default targets; an explicit `[]` selects none. On edit, omission
preserves the selection; on duplication, omission copies the source selection.
Changing defaults does not change saved jobs. The selected enabled configurations
are snapshotted at run admission, so later edits do not redirect active runs.

One notification is sent when each host finishes: `$host` is the remote host name,
`$job` the job name, and `$status` is `succeeded`, `failed`, `interrupted`, or
`cancelled`. `$output:N` selects the last N lines (1-1000); `$output` selects the
latest output, capped at 256 KiB. Only saved, secret-masked output is used. `$$`
emits a literal dollar. Substitutions occur once, and JSON substitutions are
escaped as string contents. Templates are limited to 64 KiB, rendered bodies to
1 MiB. Delivery runs independently of SSH execution. Failures are logged with
sanitized errors, without changing the job result. There are no automatic retries,
durable queue, or restart replay; shutdown cancels pending deliveries.
