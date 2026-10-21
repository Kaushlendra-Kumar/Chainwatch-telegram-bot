CREATE TABLE IF NOT EXISTS users (
    id          BIGSERIAL PRIMARY KEY,
    telegram_id BIGINT      NOT NULL UNIQUE,
    chat_id     BIGINT      NOT NULL,
    username    TEXT        NOT NULL DEFAULT '',
    first_name  TEXT        NOT NULL DEFAULT '',
    active      BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS price_alerts (
    id              BIGSERIAL PRIMARY KEY,
    user_id         BIGINT           NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    symbol          TEXT             NOT NULL,
    direction       TEXT             NOT NULL CHECK (direction IN ('above', 'below')),
    target          DOUBLE PRECISION NOT NULL CHECK (target > 0),
    created_at      TIMESTAMPTZ      NOT NULL DEFAULT now(),
    triggered_at    TIMESTAMPTZ,
    triggered_price DOUBLE PRECISION
);

CREATE INDEX IF NOT EXISTS idx_price_alerts_active ON price_alerts (symbol) WHERE triggered_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_price_alerts_user ON price_alerts (user_id);

CREATE TABLE IF NOT EXISTS wallets (
    id             BIGSERIAL PRIMARY KEY,
    chain          TEXT        NOT NULL,
    address        TEXT        NOT NULL,
    poll_cursor    TEXT        NOT NULL DEFAULT '',
    last_polled_at TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (chain, address)
);

CREATE TABLE IF NOT EXISTS wallet_subscriptions (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT           NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    wallet_id  BIGINT           NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    label      TEXT             NOT NULL DEFAULT '',
    min_usd    DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (min_usd >= 0),
    created_at TIMESTAMPTZ      NOT NULL DEFAULT now(),
    UNIQUE (user_id, wallet_id)
);

CREATE INDEX IF NOT EXISTS idx_wallet_subscriptions_wallet ON wallet_subscriptions (wallet_id);

CREATE TABLE IF NOT EXISTS wallet_events (
    id           BIGSERIAL PRIMARY KEY,
    wallet_id    BIGINT           NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    tx_hash      TEXT             NOT NULL,
    event_key    TEXT             NOT NULL,
    direction    TEXT             NOT NULL,
    counterparty TEXT             NOT NULL DEFAULT '',
    amount       DOUBLE PRECISION NOT NULL,
    symbol       TEXT             NOT NULL,
    token        TEXT             NOT NULL DEFAULT '',
    verified     BOOLEAN          NOT NULL DEFAULT TRUE,
    block_number BIGINT           NOT NULL DEFAULT 0,
    occurred_at  TIMESTAMPTZ      NOT NULL,
    notified_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ      NOT NULL DEFAULT now(),
    UNIQUE (wallet_id, tx_hash, event_key)
);

CREATE INDEX IF NOT EXISTS idx_wallet_events_pending ON wallet_events (id) WHERE notified_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_wallet_events_created ON wallet_events (created_at);

CREATE TABLE IF NOT EXISTS holdings (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT           NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    symbol     TEXT             NOT NULL,
    quantity   DOUBLE PRECISION NOT NULL CHECK (quantity > 0),
    avg_cost   DOUBLE PRECISION NOT NULL CHECK (avg_cost >= 0),
    created_at TIMESTAMPTZ      NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ      NOT NULL DEFAULT now(),
    UNIQUE (user_id, symbol)
);
