# Codex WebSocket transport and recovery

Future account-pool and recovery proposal related to
[#2472](https://github.com/yetone/magpie/issues/2472). The current branch implements
only a single-enabled-account native WS relay, per-response usage and routing
records, and observed transport display. Its current guards and behavior are in
[Gateway routing](../subsystems/gateway-routing.md), implemented by
[`codexSocketPolicy`](../../internal/gateway/codex_socket.go). Pool routing,
caps, managed keys, masking, middleware, asynchronous request hooks, recording
and model overrides still require HTTP/SSE. The pool switching and recovery
contracts below are not implemented or acceptance requirements for this issue's
current single-account scope.

## User-visible contract

Codex may keep its connection to magpie while magpie changes an upstream
connection. These are separate connections. Every upstream connection is
authenticated as exactly one account; changing accounts means acquiring a
connection authenticated as the new account, never replacing an Authorization
header on an already authenticated socket.

Usage shows client transport and upstream transport independently:
`websocket`, `sse`, `http`, or unknown when not observed. A client receiving SSE
from an upstream WebSocket is not shown as end-to-end WebSocket. Existing
records and local session files must not be classified from today's settings.
The request table follows sub2api's explicit request-type precedence:
WS, Stream, Non-streaming or Unknown. New HTTP records use the client's explicit
stream flag; legacy WS/SSE can establish WS/Stream, but HTTP alone and missing
fields stay Unknown. No agent gets a default streaming label.

## Account pools and bindings

A pool partitions connections by provider/account identity, credential generation,
endpoint, proxy/TLS configuration and compatible handshake metadata (including
session/window and protocol features). Do not put tokens in keys, logs or usage.
Connections have an exclusive session lease; being idle between turns does not
release the connection that owns a live continuation chain. In-flight response
limits and open/idle connection limits are separate controls. No I/O occurs
while a pool mutex is held.

Bind `(caller, session, stream, response chain)` to `(account, connection,
connection generation)`. A continuation with `previous_response_id` must use
that binding; an arbitrary idle connection for the same account is not a
replacement for the bound connection. Validate caller ownership of response IDs.
Credential refresh, account revocation and proxy changes invalidate incompatible
leases. Idle eviction invalidates bindings explicitly, not silently.

Sub2api reference, inspected at
[455d317d](https://github.com/Wei-Shaw/sub2api/tree/455d317d6a87d8519a52c8769a0727824c403171):
`openai_ws_pool.go` has account pools, exclusive leases, `PreferredConnID` and
`ForcePreferredConn`; `openai_ws_state_store.go` separately binds responses to
accounts and connections. These are useful boundaries. Its retry branches are
not a substitute for the replay-completeness rules below.

## Context is not a pool resource

A live socket's server-side context does not migrate with a lease or with an
account selection. Normal continuation stays on the same account and connection.
On account exhaustion, forced account changes, connection loss, eviction or
`previous_response_not_found`, enter recovery-required state.

Automatic reconstruction is allowed only when all of these are established:

- The entire current context window is available, in order, from a known full
  initial request or explicit compaction boundary through the current turn.
- No history has been truncated, evicted, skipped or missed during a restart.
- All assistant tool calls and tool results are present with matching call IDs.
  Tool results alone are not a full replay.
- Instructions, tool definitions and other state needed for that request are
  available with their actual per-turn semantics.
- Opaque reasoning, sealed task payloads, server-side references and attachments
  are known to be usable on the destination account. Possession of their bytes
  is not evidence that another account can decrypt or resolve them.
- No content or tool invocation from the attempt being retried has already been
  released to the client. Do not invisibly regenerate after partial delivery.

Only then acquire a new authenticated connection and submit the full reconstructed
input as a new response chain. Never delete `previous_response_id` and send only
the latest increment. Do not infer completeness from `input` being nonempty,
absence of tool outputs, or the upstream accepting the request.

When completeness cannot be proved, surface a typed recovery error and make
Codex restore its full context or establish a new conversation explicitly.
Validate the actual Codex recovery behavior; a custom error string is not proof
that the client will resend its history. The first implementation may refuse
automatic recovery entirely, but must say so and preserve this contract.

## Implementation sequence

1. Finish the native transport observer and end-to-end relay checks. Keep
   incompatible policies on HTTP; do not advertise the prototype as pooled WS.
2. Introduce account-partitioned connection leases, bounded lifetimes and session
   bindings. Share the existing candidate selection, model/account permissions,
   quota and concurrency rules rather than duplicating a permissive WS router.
3. Separate downstream lifetime from upstream leases. Recheck policy per
   `response.create`, not just at the HTTP upgrade. Use the same account while
   its binding is valid; loss produces recovery-required, not blind retry.
4. Implement bounded replay state only after Codex fixtures establish the full
   context and compaction boundaries. Invalidate completeness on unsupported
   frames, opaque cross-account state, truncation and interrupted responses.
5. Permit account changes with the downstream connection retained only through
   the verified recovery gate. Count every actual upstream attempt separately,
   with one logical response identity and no double-counted terminal events.
6. Integrate masking and optional recording without retaining unmasked history
   beyond their consent and retention rules; otherwise keep explicit HTTP fallback.

## Required evidence

- Normal two-turn and tool-call continuation uses one upstream account/socket.
- Concurrent sessions cannot borrow each other's response IDs or connection state.
- A switch A to B retains downstream WS but proves upstream authentication as B;
  a replayable chain contains its full history, not just the last increment.
- Increment-only input, missing tool calls, truncated history, account-bound sealed
  content, process restart and partial output all refuse automatic reconstruction.
- A lost preferred connection is not replaced by any other idle connection while
  preserving an invalid previous-response ID.
- Client cancellation, upstream failure and a normal terminal response release
  leases/slots and produce accurate, nonduplicated usage. Pool pressure is bounded.
- Actual Codex CLI/app recovers as documented; mocked protocol tests alone do not
  establish compatibility. Compare long-task latency on the same account/model.
- Client/upstream transport survive JSONL, compact row caches, CSV and GUI in all
  languages; old records remain unknown.

The isolated native smoke check used Codex CLI 0.162.1-compatible headers and a
copy of the ChatGPT sign-in under a temporary HOME, with two minimal responses
and `previous_response_id`. Both completed over upstream and downstream WS and
produced two usage rows. It did not test account switches or automatic recovery.
