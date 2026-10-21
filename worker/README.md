# ChainWatch on Cloudflare Workers

The production version of the bot: a TypeScript port of the Go code in the
repo root, so it runs on Cloudflare's free Workers plan (no credit card,
always on). Same commands, same messages, same database design.

```
src/
├── index.ts      entry point: webhook route, cron handler, /telegram/setup
├── bot.ts        command handlers (internal/bot)
├── jobs.ts       alerts, wallet polling, notifications (internal/worker)
├── store.ts      D1 / SQLite queries (internal/store)
├── price/        Binance, CoinGecko, caching service (internal/price)
├── chain/        Ethereum/Polygon/Arbitrum, Bitcoin, Solana (internal/chain)
├── telegram.ts   Bot API client
├── chart.ts      PNG charts via QuickChart
├── http.ts       fetch with retries, rate limiter, request budget (internal/httpx)
└── config.ts     env vars and secrets (internal/config)
migrations/       D1 schema
scripts/poll.mjs  local development without a public URL
```

## How it runs

| Go version | Worker |
|---|---|
| Long-running process with goroutine workers | Two Cron Triggers: every minute (alerts, wallets, notifications) and daily (cleanup) |
| gin server receives the webhook | `fetch` handler answers Telegram at once and replies in `waitUntil` |
| PostgreSQL on Neon | Cloudflare D1 (SQLite), same tables |
| Alerts checked every 30 s | Checked every minute (the shortest cron interval) |
| go-chart renders PNGs | QuickChart renders the PNG, the bot uploads it |

The free plan allows **50 outbound requests per invocation**. Every cron run
gets a budget of 48:

1. price alerts go first (usually one Binance call, plus one message per fired alert);
2. wallets are polled least-recently-polled first, keeping 8 requests back for messages;
3. new transfers are sent; if the budget can't cover a wallet's messages, its
   events are put back and go out next minute.

So a wallet is checked about once a minute while fewer than ~20 are watched.
More wallets simply take turns: each one is checked every few minutes and
nothing is lost. A Solana poll that runs out of budget, or gets rate limited,
keeps the transactions it finished and resumes from there.

## Deploy (free, no card)

Everything below runs from this `worker/` folder. Wrangler must be logged in
(`npx wrangler login`).

```bash
npm install
npx wrangler d1 create chainwatch      # once; put the id in wrangler.jsonc
npm run db:migrate                     # creates the tables in D1
```

Fill in `.prod.secrets` (git-ignored), one `NAME=value` per line:

| Secret | Where to get it |
|---|---|
| `TELEGRAM_BOT_TOKEN` | Telegram, @BotFather, `/newbot` |
| `WEBHOOK_SECRET` | Any random string |
| `ETHERSCAN_API_KEY` | etherscan.io, API Dashboard (free) |
| `COINGECKO_API_KEY` | coingecko.com, Developer Dashboard, Demo key (free) |
| `SOLANA_RPC_URL` | Optional. A free Helius URL; the public RPC rate-limits heavily |

```bash
npx wrangler deploy --secrets-file .prod.secrets
```

Then open this once in a browser. It registers the webhook and the command menu:

```
https://chainwatch-bot.<your-subdomain>.workers.dev/telegram/setup?key=<WEBHOOK_SECRET>
```

Later code changes only need `npm run deploy`. To change a secret:
`npx wrangler secret put TELEGRAM_BOT_TOKEN`.

Check it: `/healthz`, `/readyz`, `/api/v1/price/btc` on the worker URL, then
send `/start` to the bot. Logs: Cloudflare dashboard, Workers, chainwatch-bot,
Logs (or `npx wrangler tail`).

## Local development

Use a separate **dev** bot from @BotFather, so local runs never fight the
production webhook.

```bash
cp .dev.vars.example .dev.vars     # dev bot token, any WEBHOOK_SECRET, keys
npm run db:migrate:local
npm run dev                        # terminal 1: the worker on :8788
npm run poll                       # terminal 2: forwards the dev bot's messages
```

`npm run poll` removes the dev bot's webhook, pulls updates with `getUpdates`
and posts each one to the local worker with the right secret header. To run
the cron job once: `curl "http://127.0.0.1:8788/__scheduled?cron=*+*+*+*+*"`.

Wrangler only passes `.dev.vars` entries that are listed under
`secrets.required` in `wrangler.jsonc`. To use `SOLANA_RPC_URL` locally, add
it to that list.

## Tests

```bash
npm run typecheck
npm test
```

The tests port the Go suite: formatting, parsing, prices, every chain
adapter, the alert and wallet jobs, and the store. Store and job tests run
the real SQL against `node:sqlite` through a small D1 stand-in (`test/d1.ts`).

## Free-tier budget

| Resource | Free limit | Usage |
|---|---|---|
| Worker requests | 100,000 / day | 1,440 cron runs plus one per Telegram message |
| Outbound requests | 50 per invocation | Budgeted at 48, see above |
| CPU | 10 ms per invocation | Waiting on the network doesn't count; JSON parsing is the main cost |
| Cron Triggers | 5 per account | 2 |
| D1 | 5 GB, 5M rows read / 100k written per day | Rows are only written when something changes, plus one cursor update per polled wallet |
| Etherscan | 3 req/s, 100k / day | Limited to 2.5 req/s; the request budget caps it far below the daily limit |
| CoinGecko Demo | 10k / month | `/top` (cached 10 min), symbol lookups and fallback prices |
