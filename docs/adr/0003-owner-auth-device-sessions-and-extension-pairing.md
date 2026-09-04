# Use server-held opaque credentials with Device-session revocation

Visual YouTube Notes uses one database-enforced Owner, Argon2id password hashing, server-held opaque token hashes, and a fresh Device session for every password login or successful extension Pairing. A Device session is the Token family's revocation boundary: it owns one live 15-minute access token and a single-use refresh-token chain whose inactivity deadline advances by 30 days only after a successful refresh. This makes individual and global revocation immediate without introducing signed tokens or a redundant token-family table.

## Password and token material

- Passwords use Argon2id with 64 MiB memory, 3 iterations, parallelism 1, a random 16-byte salt, and a 32-byte output. The encoded hash retains its parameters so a later login can rehash after parameters increase.
- Access and refresh values are independent random 256-bit opaque tokens. PostgreSQL stores only HMAC-SHA-256 digests and a positive hash-key identifier; HMAC keys live outside PostgreSQL. Verification may use the current and previous configured keys during rotation.
- A Device session owns at most one unrevoked access row and one unconsumed, unrevoked refresh row. Consumed refresh rows remain as a parent-linked lineage for reuse detection.
- Revoked and expired session/credential records are retained for 90 days and then purged. Security events are retained for the same period; expired Pairing requests are purged after 24 hours.

## Atomic refresh and revocation

A refresh transaction looks up the presented digest, locks its Device session and refresh row, and rejects revoked or inactive sessions. If the refresh row is live, the transaction consumes it, revokes the previous access row, inserts replacement access and refresh rows, updates `last_refreshed_at` and `inactive_expires_at`, and commits before returning credentials. Ordinary API activity may update `last_seen_at` at most once every five minutes but never extends inactivity.

If the presented refresh row was already consumed, the same transaction records `reuse_detected_at`, revokes the Device session and its live credentials with reason `refresh_reuse`, and appends a security event. There is no grace window: PWA tabs serialize refresh with Web Locks and recheck session state inside the lock. All invalid, expired, revoked, and reused credentials receive the same external authentication failure; details stay in security events.

An authenticated password change keeps the initiating Device session, rotates both of its credentials, and revokes every other Device session. The server-side `reset-password` CLI has no initiating session and therefore revokes all Device sessions and rejects every unconsumed Pairing request.

## PWA cookies and CSRF

The same-origin PWA receives credentials only as cookies:

- `__Host-vyn_access`: `Secure; HttpOnly; SameSite=Lax; Path=/`, with a 15-minute lifetime.
- `__Secure-vyn_refresh`: `Secure; HttpOnly; SameSite=Lax; Path=/auth/refresh`, with a lifetime matching the Device session's current inactivity deadline.
- Neither cookie carries a `Domain` attribute.

Every Device session also has an independent random 256-bit CSRF token whose SHA-256 hash is stored. Login and successful refresh responses supply the raw CSRF token outside the cookies; the PWA retains it locally. Cookie-authenticated mutations require `X-CSRF-Token`, constant-time hash comparison, an exact `https://yt-track.mkhismkh.com` source-origin match, and a non-simple JSON content type. `GET /auth/refresh/csrf` provides a no-store recovery path using the refresh cookie. `POST /auth/refresh/logout` can revoke the session even after the access cookie expires. State-changing GET routes do not exist.

## Extension transport and Pairing

The server allows exactly one configured `chrome-extension://<stable-id>` origin. CORS is enabled only for Pairing creation/exchange, token refresh, Submission creation, and extension logout; it echoes that exact origin, allows only required methods and headers, sends `Vary: Origin`, and never allows credentials. The extension requests host permission only for `https://yt-track.mkhismkh.com/*`, stores its Token family in non-synced extension-local storage, and sends bearer credentials only to that origin. Requests that mix bearer and cookie transports are rejected.

The extension creates a random 256-bit verifier, submits its SHA-256 challenge with its extension ID and display name, and receives a random 256-bit public request ID. The authenticated PWA may approve or deny the request during its exact five-minute lifetime. Exchange verifies the configured request origin, matching extension ID, constant-time challenge equality, approval, expiry, and non-consumption before atomically consuming the request and creating an extension Device session. The response returns the only copy of its access and refresh tokens. A lost response requires a new Pairing rather than retaining recoverable bearer values server-side.

## Owner CLI and audit data

`create-owner` fails if the Owner exists. `reset-password` operates on the sole Owner. Both prompt twice on a TTY and may explicitly read one password from standard input for automation; passwords are never accepted as arguments or printed. Security events are append-only and limited to a typed event, timestamp, optional Owner and Device session, source IP, extension ID, and safe reason code—never credentials, verifiers, passwords, raw request bodies, query strings, or user-agent strings.
