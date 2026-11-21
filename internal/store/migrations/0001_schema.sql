-- Wrench schema: GitLab identities and credentials, sessions, the attempt journal with its cost history,
-- alert incidents and the cached OpenRouter catalog. Credentials are stored only as SHA-256 hashes of random tokens.

-- An allowlisted email bound to one stable GitLab identity on first sign-in.
CREATE TABLE users (
    id                bigserial   PRIMARY KEY,
    email             text        NOT NULL UNIQUE,
    issuer            text        NOT NULL,
    subject           text        NOT NULL,
    gitlab_id         bigint      NOT NULL,
    status            text        NOT NULL CHECK (status IN ('pending', 'active', 'blocked')),
    status_checked_at timestamptz,
    created_at        timestamptz NOT NULL,
    UNIQUE (issuer, subject)
);

CREATE TABLE web_sessions (
    token_hash bytea       PRIMARY KEY,
    user_id    bigint      NOT NULL REFERENCES users (id),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz
);

CREATE INDEX web_sessions_user ON web_sessions (user_id) WHERE revoked_at IS NULL;

-- Device keys; a key issued by the device login can be renewed once with its refresh token.
CREATE TABLE client_keys (
    id                 bigserial   PRIMARY KEY,
    token_hash         bytea       NOT NULL UNIQUE,
    refresh_token_hash bytea       UNIQUE,
    user_id            bigint      NOT NULL REFERENCES users (id),
    device_id          text        NOT NULL,
    device_name        text        NOT NULL DEFAULT '',
    created_at         timestamptz NOT NULL,
    expires_at         timestamptz NOT NULL,
    last_used_at       timestamptz,
    revoked_at         timestamptz
);

-- A new sign-in rotates the key of one device; other devices keep theirs.
CREATE UNIQUE INDEX client_keys_active_device ON client_keys (user_id, device_id) WHERE revoked_at IS NULL;

-- OAuth state of a web sign-in in progress; consumed exactly once by the callback. return_to is the same-origin
-- path the sign-in returns to; empty means the dashboard root.
CREATE TABLE login_flows (
    state           text        PRIMARY KEY,
    nonce           text        NOT NULL,
    verifier        text        NOT NULL,
    browser_binding bytea       NOT NULL,
    return_to       text        NOT NULL DEFAULT '',
    expires_at      timestamptz NOT NULL
);

-- One device login (RFC 8628) waiting for a signed-in user's decision. An approved code is consumed by the first poll.
CREATE TABLE device_codes (
    device_code_hash bytea       PRIMARY KEY,
    user_code_hash   bytea       NOT NULL UNIQUE,
    status           text        NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'denied', 'consumed')),
    user_id          bigint      REFERENCES users (id),
    last_poll_at     timestamptz,
    expires_at       timestamptz NOT NULL
);

-- Every device-code request deletes expired rows; the index keeps that cheap.
CREATE INDEX device_codes_expires_at ON device_codes (expires_at);

-- A client session is live while its requests arrive. client_key_id is the device key that authenticated them.
CREATE TABLE sessions (
    id            text        PRIMARY KEY,
    user_email    text        NOT NULL,
    client_key_id bigint      REFERENCES client_keys (id),
    agent         text        NOT NULL DEFAULT '',
    started_at    timestamptz NOT NULL,
    last_seen_at  timestamptz NOT NULL
);

CREATE INDEX sessions_user_last_seen ON sessions (user_email, last_seen_at DESC);
CREATE INDEX sessions_last_seen ON sessions (last_seen_at DESC);

-- One row per session and 5-minute bucket in which the client sent a request.
CREATE TABLE session_presence (
    session_id text        NOT NULL REFERENCES sessions (id),
    bucket     timestamptz NOT NULL,
    PRIMARY KEY (session_id, bucket)
);

CREATE INDEX session_presence_bucket ON session_presence (bucket);

-- One row per upstream call. Optional provider data is NULL when not reported, never zero.
CREATE TABLE attempts (
    id                 text        PRIMARY KEY,
    request_id         text        NOT NULL,
    user_email         text        NOT NULL,
    session_id         text        NOT NULL REFERENCES sessions (id),
    requested_model    text        NOT NULL,
    upstream_model     text        NOT NULL DEFAULT '',
    provider           text        NOT NULL DEFAULT '',
    inference_provider text        NOT NULL DEFAULT '',
    generation_id      text,
    started_at         timestamptz NOT NULL,
    first_token_at     timestamptz,
    finished_at        timestamptz,
    outcome            text        CHECK (outcome IN ('success', 'error', 'cancelled')),
    error_reason       text        NOT NULL DEFAULT '',
    input_tokens       bigint      CHECK (input_tokens >= 0),
    output_tokens      bigint      CHECK (output_tokens >= 0),
    reasoning_tokens   bigint      CHECK (reasoning_tokens >= 0),
    cache_read_tokens  bigint      CHECK (cache_read_tokens >= 0),
    cache_write_tokens bigint      CHECK (cache_write_tokens >= 0),
    cost               numeric(20, 9) CHECK (cost >= 0),
    cost_state         text        NOT NULL CHECK (cost_state IN ('pending', 'confirmed')),
    reconcile_at       timestamptz,
    reconcile_tries    integer     NOT NULL DEFAULT 0,
    reconcile_error    text        NOT NULL DEFAULT '',
    reconciled_at      timestamptz,
    CHECK ((cost_state = 'confirmed') = (cost IS NOT NULL)),
    CHECK ((outcome IS NULL) = (finished_at IS NULL))
);

-- A provider generation is charged once even if a result is delivered twice.
CREATE UNIQUE INDEX attempts_generation ON attempts (generation_id) WHERE generation_id IS NOT NULL;
CREATE INDEX attempts_started ON attempts (started_at DESC);
CREATE INDEX attempts_user_started ON attempts (user_email, started_at);
CREATE INDEX attempts_session_started ON attempts (session_id, started_at);
-- Finished attempts waiting for provider metadata, in due order.
CREATE INDEX attempts_reconcile_due ON attempts (reconcile_at) WHERE reconciled_at IS NULL AND reconcile_at IS NOT NULL;
-- Finished attempts of unknown cost, counted per budget window.
CREATE INDEX attempts_unsettled ON attempts (finished_at) WHERE cost_state = 'pending' AND finished_at IS NOT NULL;

-- Append-only: each row is one source's statement about an attempt's cost.
-- attempts.cost always equals the latest row for that attempt.
CREATE TABLE charge_events (
    id          bigserial      PRIMARY KEY,
    attempt_id  text           NOT NULL REFERENCES attempts (id),
    source      text           NOT NULL CHECK (source IN ('stream', 'generation', 'not_sent')),
    cost        numeric(20, 9) NOT NULL CHECK (cost >= 0),
    previous    numeric(20, 9),
    actor       text           NOT NULL DEFAULT '',
    recorded_at timestamptz    NOT NULL
);

CREATE INDEX charge_events_attempt ON charge_events (attempt_id, id);

-- An incident is one continuing alert condition; at most one is open per key.
CREATE TABLE alert_incidents (
    id               bigserial   PRIMARY KEY,
    key              text        NOT NULL,
    severity         text        NOT NULL CHECK (severity IN ('warning', 'critical')),
    user_email       text        NOT NULL DEFAULT '',
    message          text        NOT NULL,
    opened_at        timestamptz NOT NULL,
    last_seen_at     timestamptz NOT NULL,
    last_notified_at timestamptz,
    resolved_at      timestamptz,
    resolve_notified boolean     NOT NULL DEFAULT false
);

CREATE UNIQUE INDEX alert_incidents_open ON alert_incidents (key) WHERE resolved_at IS NULL;
CREATE INDEX alert_incidents_recent ON alert_incidents (opened_at DESC);

-- Last successful OpenRouter model catalog, so a restart with OpenRouter down keeps token limits and prices.
CREATE TABLE openrouter_models (
    upstream_id           text        PRIMARY KEY,
    context_length        integer     NOT NULL,
    max_completion_tokens integer     NOT NULL,
    price_prompt          text        NOT NULL DEFAULT '',
    price_completion      text        NOT NULL DEFAULT '',
    fetched_at            timestamptz NOT NULL
);
