# One model per Processing role, and provider keys sealed to the row that holds them

Each of the four Processing roles names exactly one provider and one model — no fallback list. The
API key is encrypted with AES-256-GCM directly under a versioned application master key read from a
file, with the row's own identity as additional authenticated data, so a ciphertext moved between
rows fails to authenticate rather than quietly decrypting. Nothing caches the configuration: each
Stage reads its role fresh, so a change in Settings takes effect on the next Stage with nothing to
invalidate.

Schema: [`000008_ai_role_configuration.up.sql`](../../deploy/migrations/000008_ai_role_configuration.up.sql).
Spend accounting for whatever is configured here is [ADR 0007](0007-ai-spend-ledger-and-soft-cap.md);
the note-generation request shape is [ticket #15](https://github.com/MAX-786/visual-youtube-notes/issues/15).

## A fallback list is a promise the request shape cannot keep

Spec 11 says "retry the configured model, but never silently switch providers", and the provider
survey named `claude-haiku-4-5` a "configured fallback" for `claude-sonnet-5`. Read as an ordered
list the worker walks on failure, those two are in tension. The API settles it:

- `claude-sonnet-5` takes adaptive thinking with `output_config.effort`, and **rejects**
  `budget_tokens`.
- `claude-haiku-4-5` takes `{type: "enabled", budget_tokens: N}`, and **rejects** `effort`.
- Their context windows differ by a factor of five (1M against 200K), so a transcript that fits one
  may not fit the other.

Ticket #15 sends `effort: "low"`. A fallback that swapped only the model string would send `effort`
to a model that 400s on it — failing the retry it existed to rescue, at the exact moment the primary
was already failing. Model choice and request parameters are one decision, not two.

So each role names one model, and "fallback" means _the Owner configures Haiku instead_. This also
keeps per-note cost honest: ADR 0007 snapshots the model onto every usage row, and a silent switch
would make two notes with identical configuration cost differently for no reason the Owner could
see.

The parameters therefore travel with the model, as `request_options` on the role row — validated on
save against a per-provider validator, and **passed through unchanged at call time**. Validation
belongs where the Owner is present to be told they mistyped something. A second validator at call
time would be a copy to keep in step, and its "unknown key" verdict would be wrong for precisely the
N-1 case ADR 0009 designed for, where an older build meets newer configuration. Passing through lets
the provider judge, and a provider 4xx already classifies as a permanent failure carrying the
provider's own message, which beats ours.

## Direct encryption, because there are four rows

The textbook answer is a per-row data key wrapped by a key-encryption key. Its purpose is to make
re-encryption cheap when there are many rows. There are **four**. An envelope would add a layer
whose only justification does not exist here, so the key is encrypted directly under the master key,
with `key_version` recording which one.

`AES-256-GCM` from the Go standard library, random 96-bit nonce stored as a prefix on the
ciphertext. Random 96-bit nonces are a real hazard at volume; at four secrets rewritten a handful of
times in a deployment's life, the birthday bound is not in the conversation, and a standard-library
primitive beats a dependency.

The part worth having is the **additional authenticated data: `(user_id, role, key_version)`**, not
stored anywhere but reconstructed at decrypt. Without it, a ciphertext lifted from the transcription
row into the embeddings row still decrypts, and the embeddings role starts calling Voyage with a
Deepgram key — a failure that looks like a bad key rather than a tampered row. With it, that is an
authentication failure, which is what it is.

Rotation follows from `key_version` rather than needing a scheme of its own. The master key file
holds versioned keys, newest first, so current and previous are both loaded during a rotation and
every row is readable throughout — the same shape ADR 0003 already uses for token HMAC keys, where
"verification may use the current and previous configured keys during rotation". A
`rotate-master-key` CLI command rewrites rows one at a time; spec 11 already establishes an
authenticated server CLI for `create-owner` and `reset-password`, and this belongs beside them
rather than behind an HTTP endpoint that would need protecting against remote triggering.

Ticket #21 ruled the rotation _procedure_ out of scope and handed this ticket the _capability_.
That line is honoured exactly: the schema and key file make rotation possible without a migration;
no runbook procedure is written for it.

**The API refuses to start if any `key_version` present in the table is missing from the key file.**
Booting anyway would turn a configuration problem into a per-Note decrypt error at `generating`,
where it reads like a provider fault. This is the same instinct as #21's backup sidecar refusing to
start against a missing repository.

## The master key is a file, and the spec was wrong

Spec 11 asked for the master key "supplied through the server environment". ADR 0008 already mounts
it as a Compose file secret, for a reason that is specific to this application rather than general
hygiene: **the worker spawns yt-dlp, Deno and FFmpeg constantly**, and every one of those children
inherits the environment. Environment variables are also visible in `docker inspect`. The spec
sentence has been corrected rather than reinterpreted.

## What blocks a Submission, and what does not

`note_generation` and `embeddings` are on every run's path to `ready`, so a Submission without them
can only spend the free metadata and captions Stages to manufacture a failed Note for the Owner to
clean up. Both gate Submission, Retry and Regeneration alike.

`transcription` does **not** gate anything. It is needed only when captions turn out unusable, and
that is not known until the `captions` Stage has run — gating on it would reject videos that never
needed it. It fails at its own Stage through the existing permanent-failure path.

`ai_roles_blocking_submission` is one view rather than a condition written three times, because the
Submission endpoint, Retry, Regeneration and the Settings page that has to explain the block all
need the same answer. The error names the destination — "Note generation is not configured. Add a
provider key in Settings → AI" — because spec 15 requires that error language identify the next
available action, and an unconfigured-role error without a destination is the exact failure that
rule exists to prevent.

## Verification is event-driven

A key is checked with the cheapest authenticated read the provider offers (Anthropic's `GET
/v1/models` is free), and the result is recorded as `verified_at` or `verification_error`. A failure
**warns rather than blocks the save**: blocking configuration on provider availability makes
Settings unusable during an outage the Owner is powerless to fix.

Where a provider offers no free read, the verification call is billable and **writes an
`ai_usage_events` row like any other spend**. ADR 0007's premise is that a ledger which under-counts
is worse than none, and a "just checking" call outside the ledger is precisely that.

There is no periodic re-verification. A **401 or 403 from any provider call clears that role's
`verified_at`** and records the error, so a key revoked upstream shows as revoked in Settings rather
than being inferred from a run of failed Notes. That is the same trade #21 made for yt-dlp: an exact
signal already arriving beats a scheduled check that mostly confirms nothing changed. Any change to
provider, model, endpoint or key also resets `verified_at` — a verification is about a specific
combination, not about a row.

Read-back returns a four-character `key_hint` and nothing else. A fingerprint would be a second
secret to store and rotate in order to answer a question `updated_at` already answers for a
single-Owner app.

## The endpoint field is the dangerous one

Spec 11 wants an endpoint "where applicable". Its failure mode is not a broken request — it is a
mistyped hostname receiving a live provider key on the first call. So it is validated on save:
`https` only, and private, loopback and link-local addresses refused. The schema carries the scheme
check as a backstop; the address-family check needs name resolution and lives in the application.

**There is no relaxation flag**, and that has a cost worth stating: the no-network provider doubles
(#14) cannot be reached through this field, because they are `http` on a private Compose address.
They are injected at the HTTP transport layer instead, which is the seam the unit tests already use.
A `VYN_ALLOW_INSECURE_ENDPOINTS` escape would put the one protection between a typo and an
exfiltrated credential one environment variable away from being off, on the host where the real keys
live. A future test that genuinely needs the column should get a fixture that writes the row
directly.

Changing provider, model, endpoint or key writes an `ai_role_changed` security event — reusing ADR
0003's `auth_security_events` rather than inventing a second log for one writer. The endpoint field
is what earns it: repointing a role at an attacker-controlled host is a credential-exfiltration move
that would otherwise leave only an `updated_at` bump. One event per changed field, carrying role and
field name in `reason_code`, never a value.

## Defaults are data so they can be checked

`ai_role_defaults` holds the recommended starting configuration — `deepgram/nova-3` at
`language=multi`, `anthropic/claude-sonnet-5` at `effort: low`, `voyage/voyage-4` at 1024 dimensions
— which `create-owner` copies into the Owner's rows with a null key.

It is a table rather than a constant in code so that it can be **joined against the rest of the
schema and asserted**. Two silent failures are now impossible:

- A role naming a model with **no price row** does not error; ADR 0007 files its spend under
  `unpriced_units`. The test asserts every default joins to `ai_model_prices`.
- An embeddings model whose `output_dimension` does not match `search_embeddings.embedding` is not
  caught until the first vector is stored, at which point every `indexing` Stage fails. The test
  asserts the seeded dimension equals the column's declared dimension, read from the catalog.

Both checks live in the same file and language as the things they check, which a Go-side default
plus a Go test could not manage.

`visual_analysis` gets no default. It is inactive until V1.1 and no model has been chosen; naming a
guess would be a product decision smuggled in as a seed row.

## What this ADR does not decide

- **The rotation procedure**, deliberately — see above. The capability is here; #21 ruled the
  runbook entry out of scope.
- **Which models the Owner should actually run.** The defaults are the survey's starting choice, not
  a measurement: ticket #14 defers real keys, so no model here has been called even once.
- **The Settings API and page**, which belong to the OpenAPI contract and to
  [#23](https://github.com/MAX-786/visual-youtube-notes/issues/23). This decides what they read and
  write, and that a key is never among it.
