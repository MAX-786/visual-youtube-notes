# Hand-write one OpenAPI contract, generate both ends from it, and validate requests against it

`docs/design/openapi.yaml` is authored by hand and is the only place a wire shape is defined. The
Go server is generated from it as a strict `chi` server, so an operation the implementation forgets
is a compile error rather than a 404, and every request is additionally validated against the
document at runtime, which is spec 15's "strict input validation" answered once instead of per
handler. The TypeScript client is generated from the same file. One error envelope with a closed set
of typed codes replaces the error codes the earlier tickets had each invented on their own.

Ticket [#28](https://github.com/MAX-786/visual-youtube-notes/issues/28). The contract is
[`openapi.yaml`](../design/openapi.yaml); [`openapi.test.mjs`](../design/openapi.test.mjs) is what
keeps it honest.

## Hand-written, because the contract precedes the code

[ADR 0001](0001-repository-layout-and-toolchain.md) already rejected deriving the contract from Go
annotations, because that would make one implementation's transport details the cross-client
contract. The same reasoning settles the finer question this ticket asked -- how much is authored and
how much derived. Nothing here is derived: the contract is a design artifact written before either
implementation exists, from fourteen ADRs and their design drafts, and the two implementations are
derived from _it_. The one derived thing in the repository is the pair of generated files, which CI
regenerates and diffs so the contract cannot change without both ends changing in the same commit.

## Generated _and_ validated, because they catch different mistakes

Generating the Go server (`strict-server`, `chi-server`, `models`) makes the operation inventory a
compile-time fact: the handler type implements an interface with one method per operation, and a
missing or mis-typed one does not build. What generation cannot do is reject a request body that
names an unknown enum value or an out-of-range integer -- the generated types happily hold them. A
request-validation middleware driven by the same document does, before any handler runs, and turns
the rejection into the `invalid_request` envelope with the failing field in `details`. The two
mechanisms overlap on almost nothing, which is why both are used.

One operation is excluded from generation: `GET /events`. A Server-Sent Events stream has to flush
after every frame and hold the connection open across heartbeats, and a generated strict handler
returns one response object and finishes. The contract still documents the stream and the schemas of
its four frames, and `oapi-codegen.yaml` names the exclusion so the test can assert it stays honest;
the implementation mounts the handler by hand on the same router. Exports are _not_ excluded: a
streamed `io.Reader` body is exactly what the generated response type carries.

Both generators were run against the finished document rather than assumed to cope with it. OpenAPI
3.1's `type: [string, "null"]` and the `oneOf`-with-`null` form for nullable references both come
out as pointers in Go and as `| null` in TypeScript; the discriminated unions come out as
`json.RawMessage` unions with typed accessors, and as TypeScript unions that narrow on the
discriminator. Money is a decimal string on the wire, because the ledger is `numeric(16,10)` and a
JSON number would round a twenty-token query embedding to nothing ([ADR 0007](0007-ai-spend-ledger-and-soft-cap.md)).

## The Revision payload is a structural mirror, kept in step by a test

The normative schema for a Diagram is `diagram.schema.json`, and it uses `if`/`then`,
`unevaluatedProperties`, `contains` with `minContains`, and `$defs` -- none of which either generator
can turn into a type, and none of which the OpenAPI document can reference by file without one of
them failing on the keywords. Three options were weighed:

- **Opaque.** Declare the payload `type: object` and let each end unmarshal into its own type. Cheap,
  and it leaves the PWA with `unknown` where it needs `nodes[].label`.
- **External `$ref`.** One source of truth, and a generator that either fails or emits `interface{}`
  the moment it meets `unevaluatedProperties`.
- **A structural mirror.** The same properties, enums, and cardinalities written in the subset both
  generators support, with the semantic rules -- tree structure, grounding, the 12-visible cap --
  left to the server's validator, where [ADR 0004](0004-revision-mutation-and-conflict-model.md)
  already put them.

The mirror was chosen, and the drift it invites is bounded by `openapi.test.mjs`: every example the
normative schema accepts must pass the mirror, the two cardinality caps the mirror claims to carry
must still reject their examples, and the stored payload `toStoredPayload()` produces from the
generation fixture must validate as a `RevisionPayload`. The test paid for itself on its first run,
finding that the mirror required `speaker_claim` where the normative schema makes it optional with a
default.

## One error envelope, with the code as the contract

Seven tickets had each invented error codes -- `note_trashed`, `pairing_not_approved`,
`semantic_unavailable`, `validation_failed` -- with no shared shape. The contract fixes one:
`{ "error": { "code", "message", "details" } }`, where `code` is a closed enum, the HTTP status is
fixed per code in a table the test checks every code appears in, `message` is written for a person
and names the next action as spec 15 requires, and `details` carries keys fixed per code so a client
can act on `active_revision_id` or `server_text` without parsing prose.

Every operation declares only its success responses and a `default` error response. Declaring five
error statuses on seventy-six operations would generate several hundred response types that all hold the
same envelope; the `x-error-codes` extension lists per operation which codes it can return, which is
the information those types would have carried.

Two judgement calls inside the envelope are worth recording. First, a mutation _refusal_ is an error,
not a third outcome beside `applied` and `conflicted`: the ledger records rejections, but a refusal
writes no Revision, so it has no `revision_id` to return, and `openapi-fetch` puts typed errors in
`error` and successes in `data`, which is the split the client wants. Second, several codes are
deliberately uniform where a more specific answer would be an oracle: `unauthenticated` for every
credential failure ([ADR 0003](0003-owner-auth-device-sessions-and-extension-pairing.md)), and
`pairing_rejected` for mismatch, replay, and an already-consumed request alike.

## The extension's authentication lives under `/pairing`

Spec 11 scopes the PWA's refresh cookie to `/auth/refresh`, which fixes that path for cookie
transport. The extension's refresh sends its token in a body and receives the replacement pair in
the response -- a different operation with a different shape, and it belongs beside the rest of the
extension's authentication rather than sharing a path with the cookie flow. So `POST /pairing/refresh`
and `POST /pairing/logout` join `POST /pairing/requests` and `POST /pairing/exchange`, and spec 13's
`/pairing/*` boundary is corrected to say so.

The CORS allowlist is derived from the contract rather than maintained beside it: exactly the
operations marked `x-extension-callable: true`, which the test pins to six. ADR 0003 listed five --
Pairing creation and exchange, token refresh, Submission creation, and extension logout -- and
[ADR 0013](0013-chromium-extension-structure-and-pairing.md) then gave the extension an Undo message
without noticing the endpoint behind it was not on the list. `DELETE /submissions/{noteId}` is
added; this refines ADR 0003's list rather than rewriting it.

## When `held` comes back from a Submission

ADR 0013 has the extension render a `held` outcome from the synchronous response, and
[ADR 0007](0007-ai-spend-ledger-and-soft-cap.md) says the Soft cap gate cannot fire at enqueue
because the estimate is not computable yet. Both are right, and the contract states how: `held` is
returned when month-to-date spend already meets the cap at submission time, so the run is _certain_
to stop at the gate. The Note and its free Stages are created exactly as for `created`; the hold
itself is placed after `captions` with a real estimate, as ADR 0007 requires. What differs is only
what the client is told -- "saved, needs your confirmation" rather than "added" -- and that no Undo
is offered, since the Owner has to visit the PWA regardless. Retry and Regeneration, where the
Transcript is already in hand, run the gate synchronously and answer `held` with the estimate.

## Mounting, and what is not in the document

Every path is served under `/api` through a single `servers` entry ([ADR 0008](0008-deployment-topology.md)).
The API's `/healthz`, which Compose probes at the container's root, is not a product resource and is
not in the contract; the implementation serves it beside the mounted router.

`/tags` is added as a top-level boundary. Spec 13 folded Tags into `/topics/*`, which would have put
Tag operations under a path that names a different entity; the spec's list is corrected.

## What this ADR corrects

- **Spec 13**, "Recommended resource/API boundaries": `/pairing/*` now covers extension token refresh
  and logout, and `/tags` is listed.
- **ADR 0003's CORS list** gains Submission Undo, as above.

## What this ADR does not decide

- **Rate-limit values and mechanism.** The contract reserves `rate_limited` at `429` with
  `Retry-After` and nothing more; ticket #29 owns the rest.
- **Contract versioning beyond `1.0.0`.** There is one client and one server, shipped together, and
  a breaking change regenerates both ends in the same commit. A policy for independent evolution can
  wait until there is a second consumer.
- **The SSE handler's implementation** -- leader election, heartbeat, and revocation are
  [ADR 0012](0012-pwa-shell-live-updates-and-offline-cache.md)'s; this only fixes the frames.
