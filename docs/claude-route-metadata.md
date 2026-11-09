# Claude route metadata

`observability.logs.route-metadata-log` defaults to `false`. It enables only
`route_metadata_v1` application log events for actual Claude messages and
upstream token-count HTTP sends. Local token estimation produces no route event.
It does not enable debug logging, full request capture, or any management API.

## Correlation and limits

Each event contains the complete gateway-generated `route_request_id`, a random
`route_attempt_id` for that executor invocation, and `route_hop` for each actual
transport call, including redirects. Authentication/model retries invoke the
executor again and therefore receive another attempt ID. Existing CPA trace
headers and short request-ID log prefixes remain unchanged.

`route_session_binding=explicit_uuid` requires an explicit Claude session UUID
from the incoming session header or Claude metadata, with no conflicting header
or payload UUID and no subagent/parent identity. The session reference is lowercase
hex SHA-256 of `cpa:route-session:v1`, a NUL byte, and the lowercase UUID. An owner
can calculate the same digest offline from a native session UUID. This binds only
the UUID observed by the gateway; callers that do not send their native UUID
remain unproven. Missing, non-UUID, conflicting, and subagent identities are
`unavailable` or `ambiguous`, with no session reference. Affinity-derived identities
are never presented as native-session evidence.

`route_auth_ref` uses SHA-256 of `cpa:route-auth:v1`, a NUL byte, and the already
selected opaque auth ID. It does not hash a credential or reveal an account
identity. Only a numeric existing configuration index is copied; no management
credential object or auth file is read. Indices can change on configuration reload
and are not a configuration revision. A valid compiled commit is recorded as
`route_build`; otherwise it is omitted. No configuration hash or revision is
claimed.

`provider` is `claude` or `unknown`; it is an executor label, not a supplier or
account attribution. `upstream_host` and `upstream_scheme` come from the actual
outgoing request at every transport hop. Userinfo, port, path, query, fragment,
proxy configuration, headers, prompt, arbitrary model names, errors and response
bodies are excluded. Only HTTP(S) and valid ASCII DNS names or IPs are logged.
Operators must check that upstream hostnames themselves contain no secrets before
enabling this feature. A gateway hostname does not reveal its internal provider
or account.

A `start` event precedes RoundTrip and an `end` event records response headers or
an enumerated transport/cancellation outcome. HTTP status is an integer. An end
event is **not** successful stream completion: it does not wait for or consume
the response body. Transport wrappers reuse the original connection pool and
preserve redirects, timeout, cancellation, body streaming and idle cleanup.

This implementation cannot establish any earlier request's missing association,
including the previously observed 403. It does not restore authentication or
prove any permission acceptance criterion.

## Offline validation

Run from an isolated checkout with fixture credentials only:

```sh
go test ./internal/config ./internal/logging ./internal/runtime/executor/helps ./internal/runtime/executor -run 'RouteMetadata|CPATrace|AntigravityConnectionTrace' -count=1
go test -race ./internal/config ./internal/logging ./internal/runtime/executor/helps ./internal/runtime/executor -run 'RouteMetadata|CPATrace|AntigravityConnectionTrace' -count=1
go build -o test-output ./cmd/server
rm test-output
```

Tests cover canonical config loading/round-trip and default-off behavior, actual
Claude executor hooks, formatter leakage canaries, parallel session isolation,
redirect hosts/policy, cancellation, early streaming headers and immutable
metadata snapshots. The HTTP fixtures use loopback servers or fake transports;
no provider login or model request is required.

## Release and rollback handoff

This change has not been deployed. Use the existing runtime coordinator and
`DEPLOY.md` build script, retaining official version/commit/build-date metadata.
Before deployment, reconcile this diff with the actual deployed source revision,
record the approved original artifact hash and the new artifact hash, and verify
that the original artifact is available for rollback. Do not infer this from an
arbitrarily named previous binary or create unsolicited credential/config copies.

Initial binary replacement requires the existing service update procedure. A
plain systemd restart can interrupt in-flight HTTP/SSE requests. Use a proven
platform drain/handoff procedure if available; otherwise schedule a runtime window
with the known interruption scope. Do not force active sessions closed for this
diagnostic. Do not claim an untested hot reload is harmless: existing config
watching reloads clients. Keep the flag false during installation until the
existing runtime chain explicitly schedules activation. Do not use the runbook's
model smoke commands without separate authorization for a future model request.

Rollback through that same drain/update procedure by restoring the approved,
hash-verified original binary, or disabling the flag through a separately verified
configuration lifecycle. Either may require a restart; configuration hot reload
is not certified by these tests. Preserve existing evidence, check service health
with the existing non-model health procedure, and leave all account credentials,
manager defaults and active sessions intact. Health alone proves no model access.
