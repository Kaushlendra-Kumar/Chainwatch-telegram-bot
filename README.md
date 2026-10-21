# ChainWatch 🔔

A Telegram bot for crypto: live prices, price alerts, charts, **on-chain wallet tracking** (Ethereum, Polygon, Arbitrum, Bitcoin, Solana) and portfolio P&L. Runs 24/7 on Cloudflare's free plan, no credit card.

```
/price btc eth sol          /alert btc above 100k        /chart eth 30d
/watch eth 0xd8dA…6045 Vitalik      /minusd 1 10000      /portfolio
```

## Two versions in this repo

| Folder | What | Use it for |
|---|---|---|
| [`worker/`](worker/) | TypeScript on Cloudflare Workers + D1 | **Production.** Free and always on. Deploy steps: [worker/README.md](worker/README.md) |
| repo root (`cmd/`, `internal/`) | The original Go version (telebot, gin, PostgreSQL) | Reference, or running on your own PC/VPS |

Both have the same commands, messages and database design. The worker is a
port of the Go code; [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) explains the
design, and [worker/README.md](worker/README.md) explains what changed for
Cloudflare (cron instead of goroutines, D1 instead of Postgres, a per-run
request budget).

## Quick start (production)

1. Telegram → **@BotFather** → `/newbot` → copy the token. Make a second
   bot for local testing.
2. Free keys: [Etherscan](https://etherscan.io/apis) (EVM wallets) and a
   [CoinGecko Demo key](https://www.coingecko.com/en/api/pricing) (`/top`, price fallback).
   Optional: a [Helius](https://www.helius.dev) RPC URL for Solana.
3. Deploy:
   ```bash
   cd worker
   npm install
   npm run db:migrate
   npx wrangler deploy --secrets-file .prod.secrets
   ```
4. Open `https://chainwatch-bot.<subdomain>.workers.dev/telegram/setup?key=<WEBHOOK_SECRET>` once.
5. Send `/start` to the bot.

## Commands

| Command | Example | What it does |
|---|---|---|
| `/price` | `/price btc eth sol` | Prices with 24h change (up to 10 symbols) |
| `/top` | `/top` | Top 10 by market cap |
| `/chart` | `/chart btc 30d` | PNG chart: `1d 7d 30d 90d 1y` |
| `/alert` | `/alert eth below 2.5k` | One-shot alert when price crosses a level |
| `/alerts`, `/delalert` | `/delalert 4` | List / delete alerts |
| `/watch` | `/watch sol <address> My wallet` | Notify on new confirmed transfers |
| `/wallets`, `/unwatch` | `/unwatch 2` | List / stop watching |
| `/minusd` | `/minusd 2 10000` | Only notify transfers ≥ $10,000 (0 = all) |
| `/balance` | `/balance btc bc1q…` | Native coin balance + USD value |
| `/chains` | `/chains` | Supported chain ids |
| `/add`, `/sell`, `/remove` | `/sell btc 0.1 70000` | Record trades (weighted avg cost, realized P&L) |
| `/portfolio` | `/portfolio` | Value, P&L, allocation |

Chain ids: `eth`, `polygon`, `arbitrum`, `btc`, `sol` (aliases: `ethereum`, `matic`, `bitcoin`, `solana`). Numbers accept `100k`, `1.5m`, `$2,500`.

HTTP endpoints: `GET /healthz`, `GET /readyz`, `GET /api/v1/price/:symbol`, `POST /telegram/webhook`, `GET /telegram/setup?key=…`.

## Running the Go version locally

Needs Go 1.25+ and a PostgreSQL URL (free on [Neon](https://neon.com), or `docker compose up -d`).

```bash
cp .env.example .env   # dev bot token + DATABASE_URL + keys
go test ./...
go run ./cmd/bot
```

It uses long polling locally, so no public URL is needed. `render.yaml` and
the `Dockerfile` are kept for hosts that run containers; Render's free tier
needs a card and sleeps, which is why production moved to Cloudflare.

## Troubleshooting

| Symptom | Fix |
|---|---|
| Bot silent after deploy | Open `/telegram/setup?key=…` again; check `npx wrangler tail` |
| `409 Conflict: terminated by other getUpdates` | The same token is used twice. Use separate dev and prod bots. |
| `Free API access is not supported for this chain` | That chain needs a paid Etherscan plan; remove it from `EVM_CHAINS`. |
| Solana polls log `429` | Set `SOLANA_RPC_URL` to a Helius URL. |
| `/top` fails | Set `COINGECKO_API_KEY` (anonymous access is heavily throttled). |
