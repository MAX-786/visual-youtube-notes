BEGIN;

-- Ticket #29: rate limiting. Decisions and reasoning live in
-- docs/adr/0016-rate-limits-declared-in-the-contract.md; the classes and their
-- values live in docs/design/openapi.yaml under x-rate-limit-classes, which the
-- API reads at boot. Nothing about a limit is stored here.
--
-- What is stored is the trace a refused burst leaves. A request refused by a
-- rate limit is rejected authentication activity in exactly the sense ADR 0003
-- retains -- a credential-guessing run shows up as rate_limited on login long
-- before it shows up as anything else. One event is written per (class, key)
-- on the transition INTO the limited state, never per refused request: a burst
-- that produced an insert per 429 would turn the limiter into the write
-- amplifier it exists to prevent. The class name travels in reason_code, which
-- the existing format constraint already restricts to a safe token; the key is
-- whichever of source_ip and device_session_id the row carries.
ALTER TYPE auth_security_event_type ADD VALUE 'rate_limited';

COMMENT ON TYPE auth_security_event_type IS
    'Typed, credential-free authentication events. rate_limited is written once per transition into a limited state, with the rate-limit class in reason_code.';

COMMIT;
