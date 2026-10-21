# ChainWatch Architecture

This describes the Go version. The Cloudflare Workers port in `worker/` keeps
the same design (one poll per wallet, idempotent events, atomic claims,
token allowlists); [worker/README.md](../worker/README.md) covers what changed:
cron triggers instead of goroutines, D1 instead of Postgres, and a request
budget per run.

## System overview

```
                Telegram users
                      │
     webhook (prod) / long polling (dev)
                      │
┌─────────────────────▼─────────────────────────────────────────┐
│                    chainwatch (one Go binary)                 │
│                                                               │
│  gin HTTP server ── /telegram/webhook ──► telebot router      │
│   /healthz /readyz /api/v1/price           │ middleware:      │
│                                            │ recover → private│
│                                            │ → throttle → user│
│                                            ▼                  │
│                                     command handlers          │
│                                     │      │       │          │
│        ┌────────────────────────────┘      │       └───────┐  │
│        ▼                                   ▼               ▼  │
│  price.Service ◄──────────────┐     chain.Registry      store │
│  cache + fallback             │     eth/polygon/arb     (pgx) │
│  Binance → CoinGecko → peg    │     btc / sol              ▲  │
│        ▲                      │          ▲                 │  │
│        │          Refresh()   │          │                 │  │
│  ┌─────┴────────┐  signal  ┌──┴──────────┴─┐  ┌─────────┐   │  │
│  │ AlertWorker  │◄─────────│ WalletWorker  │  │ Janitor │───┘  │
│  │ every 30s    │          │ every 60s     │  │ daily   │      │
│  └──────┬───────┘          └──────┬────────┘  └─────────┘      │
│         └──────── Notifier (25 msg/s, flood retry) ◄──┘        │
└───────────────────────────────────────────────────────────────┘
          │                │                │
     PostgreSQL       Etherscan V2      mempool.space / Solana RPC
       (Neon)         Binance, CoinGecko
```

## Package layout

| Package | Responsibility |
|---|---|
| `cmd/bot` | Entry point, signal handling |
| `internal/app` | Wiring and lifecycle (start order, graceful shutdown) |
| `internal/config` | Env loading and validation, webhook secret derivation |
| `internal/httpx` | HTTP client with retries/backoff, rate limiter, daily budget |
| `internal/price` | Provider interface, Binance, CoinGecko, caching service |
| `internal/chain` | `Tracker` interface and per-chain adapters, token allowlists |
| `internal/store` | Postgres repository, embedded migrations |
| `internal/worker` | Alert engine, wallet engine, janitor |
| `internal/bot` | Telegram handlers, middleware, parsing, notifier |
| `internal/chart` | PNG rendering |
| `internal/server` | gin routes |

Dependencies point inward: `bot` and `worker` depend on `store`, `price`, `chain`; nothing depends on `bot`. Workers talk to Telegram only through the `Notifier` interface, which keeps them unit-testable with fakes.

## Key decisions

### 1. Hot state in memory, Postgres for durability
Workers load active alerts and subscriptions once, then evaluate them in memory every tick. The database is touched only when state changes: a user command, a fired alert, or new on-chain events. Handlers call `Refresh()` after a change, which sends a non-blocking signal on a buffered channel; the worker goroutine reloads on its own loop. Each worker's state is owned by a single goroutine, so there are no locks.

Why: Neon's free tier meters compute hours and scales to zero after 5 idle minutes. Polling the DB every 30s would keep it awake permanently and exhaust the quota mid-month.

### 2. One poll per wallet, fan-out to subscribers
`wallets` (unique chain + address, holds the poll cursor) is separate from `wallet_subscriptions` (user, label, threshold). Ten users watching the same whale cost one API poll, not ten.

### 3. Chain adapters behind one interface
```go
type Tracker interface {
    Normalize(address) (string, error)
    Bootstrap(ctx, address) (cursor, error)
    Poll(ctx, address, cursor) ([]Event, newCursor, error)
    Balance(ctx, address) (Balance, error)
}
```
The cursor is an opaque string: a block number for EVM, a block height for Bitcoin, a transaction signature for Solana. Adding a chain means one new file plus a registry entry.

| Chain | Source | Cursor strategy |
|---|---|---|
| EVM | Etherscan V2 `txlist` + `tokentx`, newest 100 | Max block seen; each poll re-reads a few blocks of overlap to absorb indexer lag |
| Bitcoin | Esplora `/address/:a/txs`, paginated | Max confirmed height; only confirmed txs |
| Solana | `getSignaturesForAddress(until)` + `getTransaction` | Newest signature; balance deltas from pre/post balances |

Bootstrap sets the cursor to the current head so watching a wallet never replays its history.

### 4. Idempotent events + transactional outbox
`wallet_events` has `UNIQUE (wallet_id, tx_hash, event_key)`. Overlapping polls, restarts and two instances running during a deploy all produce `ON CONFLICT DO NOTHING`, never duplicates. Events and the cursor are saved in one transaction.

Delivery is a separate step: `UPDATE … SET notified_at = now() WHERE id IN (SELECT … FOR UPDATE SKIP LOCKED) RETURNING …` atomically claims pending events, so concurrent instances never send the same notification twice. The trade-off is at-most-once delivery: a crash between claim and send loses that message.

### 5. Atomic alert claims with release on failure
An alert fires via `UPDATE … WHERE id = $1 AND triggered_at IS NULL`. Only one claimer wins. If the Telegram send fails transiently the claim is released and retried next tick; if the user blocked the bot, the user is deactivated and the alert dropped.

### 6. Anti-spoofing for token transfers
Scammers airdrop fake tokens named `USDT` to look like large deposits. Token USD values come only from a per-chain contract allowlist (`chain/tokens.go`). Unknown contracts are shown as "unverified" with no dollar value, and any `/minusd` threshold filters them out. Zero-value transfers (address poisoning) are skipped.

### 7. Price provider chain
Binance `/ticker/price` returns every pair in one request, so one cached call (15s) serves all users and both workers. Missing symbols fall back to CoinGecko (cached 5 min to protect the monthly quota), and stablecoins fall back to a $1 peg. Unknown symbols are negatively cached for 30 minutes.

### 8. Protecting free-tier quotas
- Shared Etherscan limiter (2.5 rps under the 3 rps cap) plus a hard daily budget.
- Per-user limits on alerts and wallets, and a per-user command throttle.
- Telegram send limiter at 25 msg/s, honoring `retry_after` on flood errors.

### 9. Webhook security and lifecycle
The webhook secret is a SHA-256 hex digest of `WEBHOOK_SECRET` (or the token), which satisfies Telegram's allowed character set even if Render generates base64. Requests without the matching header get 401 (constant-time compare). The HTTP server starts before `setWebhook`, and shutdown never deletes the webhook, so zero-downtime deploys on Render hand over cleanly.

## Data model

```
users ──< price_alerts
  │
  ├──< holdings
  │
  └──< wallet_subscriptions >── wallets ──< wallet_events
```

## Known limitations and next steps

- EVM internal transactions (ETH received from contracts) and NFTs aren't tracked; add `txlistinternal` and `tokennfttx`.
- Bitcoin alerts wait for confirmation; mempool alerts would be faster but can be replaced via RBF.
- Token balances aren't shown by `/balance` (Etherscan's token balance endpoint is paid).
- Prices are stored as `DOUBLE PRECISION`, which is display-grade, not accounting-grade.
- Upgrade path for real-time prices: Binance WebSocket streams instead of 30s polling.
- Inline keyboard buttons for delete/unwatch would improve mobile UX.
