-- SQLite (Cloudflare D1) port of internal/store/migrations/001_init.sql.
-- Timestamps are unix seconds.

CREATE TABLE IF NOT EXISTS users (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    telegram_id INTEGER NOT NULL UNIQUE,
    chat_id     INTEGER NOT NULL,
    username    TEXT    NOT NULL DEFAULT '',
    first_name  TEXT    NOT NULL DEFAULT '',
    active      INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at  INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE INDEX IF NOT EXISTS idx_users_chat ON users (chat_id);

CREATE TABLE IF NOT EXISTS price_alerts (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    symbol          TEXT    NOT NULL,
    direction       TEXT    NOT NULL CHECK (direction IN ('above', 'below')),
    target          REAL    NOT NULL CHECK (target > 0),
    created_at      INTEGER NOT NULL DEFAULT (unixepoch()),
    triggered_at    INTEGER,
    triggered_price REAL
);

CREATE INDEX IF NOT EXISTS idx_price_alerts_active ON price_alerts (symbol) WHERE triggered_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_price_alerts_user ON price_alerts (user_id);

CREATE TABLE IF NOT EXISTS wallets (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    chain          TEXT    NOT NULL,
    address        TEXT    NOT NULL,
    poll_cursor    TEXT    NOT NULL DEFAULT '',
    last_polled_at INTEGER NOT NULL DEFAULT 0,
    created_at     INTEGER NOT NULL DEFAULT (unixepoch()),
    UNIQUE (chain, address)
);

CREATE INDEX IF NOT EXISTS idx_wallets_polled ON wallets (last_polled_at);

CREATE TABLE IF NOT EXISTS wallet_subscriptions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    wallet_id  INTEGER NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    label      TEXT    NOT NULL DEFAULT '',
    min_usd    REAL    NOT NULL DEFAULT 0 CHECK (min_usd >= 0),
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    UNIQUE (user_id, wallet_id)
);

CREATE INDEX IF NOT EXISTS idx_wallet_subscriptions_wallet ON wallet_subscriptions (wallet_id);

CREATE TABLE IF NOT EXISTS wallet_events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    wallet_id    INTEGER NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    tx_hash      TEXT    NOT NULL,
    event_key    TEXT    NOT NULL,
    direction    TEXT    NOT NULL,
    counterparty TEXT    NOT NULL DEFAULT '',
    amount       REAL    NOT NULL,
    symbol       TEXT    NOT NULL,
    token        TEXT    NOT NULL DEFAULT '',
    verified     INTEGER NOT NULL DEFAULT 1,
    block_number INTEGER NOT NULL DEFAULT 0,
    occurred_at  INTEGER NOT NULL,
    notified_at  INTEGER,
    created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    UNIQUE (wallet_id, tx_hash, event_key)
);

CREATE INDEX IF NOT EXISTS idx_wallet_events_pending ON wallet_events (id) WHERE notified_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_wallet_events_created ON wallet_events (created_at);

CREATE TABLE IF NOT EXISTS holdings (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    symbol     TEXT    NOT NULL,
    quantity   REAL    NOT NULL CHECK (quantity > 0),
    avg_cost   REAL    NOT NULL CHECK (avg_cost >= 0),
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
    UNIQUE (user_id, symbol)
);
