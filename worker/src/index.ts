import { handleUpdate } from "./bot";
import { buildChains, loadConfig, telegramSecret, type Config, type Env } from "./config";
import { NotFoundError } from "./errors";
import { Budget, Http } from "./http";
import { dispatchEvents, pollWallets, runAlerts } from "./jobs";
import { newLogger } from "./log";
import { Binance } from "./price/binance";
import { CoinGecko } from "./price/coingecko";
import { PriceService } from "./price/service";
import { validSymbol } from "./price/types";
import { Store, type Database } from "./store";
import { commandMenu, Telegram, type TgUpdate } from "./telegram";

const WEBHOOK_PATH = "/telegram/webhook";
const JANITOR_CRON = "17 3 * * *";

// The free plan allows 50 outbound requests per invocation; keep a little
// headroom.
const REQUEST_BUDGET = 48;
// Requests a cron run keeps back from wallet polling for notifications.
const NOTIFY_RESERVE = 8;

function services(env: Env, cfg: Config, budget: Budget, component: string) {
  const log = newLogger(cfg.logLevel, { component });
  const http = new Http(budget);
  const store = new Store(env.DB as unknown as Database);
  const gecko = new CoinGecko(http, cfg.coingeckoURL, cfg.coingeckoKey);
  const prices = new PriceService(new Binance(http, cfg.binanceURL), gecko, gecko, log);
  const chains = buildChains(cfg, http, log);
  const tg = new Telegram(http, cfg.botToken, cfg.telegramAPI);
  return { log, http, store, prices, chains, tg };
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

function safeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

export default {
  async fetch(req: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    const url = new URL(req.url);
    const cfg = loadConfig(env);
    const s = services(env, cfg, new Budget(REQUEST_BUDGET), "http");

    if (req.method === "POST" && url.pathname === WEBHOOK_PATH) {
      if (!cfg.botToken) return json({ error: "TELEGRAM_BOT_TOKEN not set" }, 500);
      const got = req.headers.get("X-Telegram-Bot-Api-Secret-Token") ?? "";
      if (!safeEqual(got, await telegramSecret(cfg))) return new Response(null, { status: 401 });
      let update: TgUpdate;
      try {
        update = await req.json();
      } catch {
        return new Response(null, { status: 400 });
      }
      // Answer Telegram right away; the reply is sent as a separate request.
      ctx.waitUntil(
        handleUpdate(update, { ...s, maxAlerts: cfg.maxAlerts, maxWallets: cfg.maxWallets }).catch((err) =>
          s.log.error("handle update", { err }),
        ),
      );
      return new Response(null, { status: 200 });
    }

    if (req.method !== "GET" && req.method !== "HEAD") return new Response(null, { status: 405 });

    if (url.pathname === "/" || url.pathname === "/healthz") {
      return json({ status: "ok" });
    }

    if (url.pathname === "/readyz") {
      try {
        await s.store.ping();
        return json({ status: "ready" });
      } catch {
        return json({ status: "db unavailable" }, 503);
      }
    }

    const priceMatch = url.pathname.match(/^\/api\/v1\/price\/([^/]+)$/);
    if (priceMatch) {
      const symbol = decodeURIComponent(priceMatch[1]!);
      if (!validSymbol(symbol)) return json({ error: "invalid symbol" }, 400);
      try {
        const q = await s.prices.quote(symbol, true);
        return json({ symbol: q.symbol, price_usd: q.price, change_24h: q.change24h, source: q.source });
      } catch (err) {
        if (err instanceof NotFoundError) return json({ error: "symbol not found" }, 404);
        return json({ error: "price sources unavailable" }, 502);
      }
    }

    // Registers the webhook and the command menu. Open it once after each
    // deploy that changes the URL or the secret:
    //   https://<worker>/telegram/setup?key=<WEBHOOK_SECRET>
    if (url.pathname === "/telegram/setup") {
      if (!cfg.webhookSecret || !safeEqual(url.searchParams.get("key") ?? "", cfg.webhookSecret)) {
        return json({ error: "set WEBHOOK_SECRET and pass it as ?key=" }, 401);
      }
      try {
        const webhook = url.origin + WEBHOOK_PATH;
        await s.tg.call("setWebhook", {
          url: webhook,
          secret_token: await telegramSecret(cfg),
          allowed_updates: ["message"],
          max_connections: 20,
        });
        await s.tg.call("setMyCommands", { commands: commandMenu });
        const me = await s.tg.call<{ username: string }>("getMe");
        return json({ ok: true, bot: "@" + me.username, webhook });
      } catch (err) {
        return json({ ok: false, error: (err as Error).message }, 502);
      }
    }

    return json({ error: "not found" }, 404);
  },

  async scheduled(event: ScheduledController, env: Env, ctx: ExecutionContext): Promise<void> {
    const cfg = loadConfig(env);
    const budget = new Budget(REQUEST_BUDGET);
    const s = services(env, cfg, budget, "cron");

    if (event.cron === JANITOR_CRON) {
      ctx.waitUntil(
        s.store
          .cleanup(cfg.retentionDays * 86_400)
          .then((res) => s.log.info("cleanup done", { ...res }))
          .catch((err) => s.log.error("cleanup", { err })),
      );
      return;
    }
    if (!cfg.botToken) {
      s.log.error("TELEGRAM_BOT_TOKEN not set");
      return;
    }

    const run = async () => {
      await runAlerts(s.store, s.prices, s.tg, budget, s.log).catch((err) => s.log.error("alerts", { err }));
      await pollWallets(s.store, s.chains, budget, s.log, NOTIFY_RESERVE).catch((err) => s.log.error("poll wallets", { err }));
      await dispatchEvents(s.store, s.chains, s.prices, s.tg, budget, s.log, cfg.notifyBatchLimit).catch((err) =>
        s.log.error("dispatch events", { err }),
      );
      s.log.debug("cron done", { requests: budget.used });
    };
    ctx.waitUntil(run());
  },
} satisfies ExportedHandler<Env>;
