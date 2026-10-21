import type { Registry, Tracker } from "./chain/chain";
import { renderChart } from "./chart";
import { InvalidAddressError, NotFoundError } from "./errors";
import * as format from "./format";
import type { Http } from "./http";
import { triggered } from "./jobs";
import type { Logger } from "./log";
import { parseDays, parseDirection, parseID, parseNumber } from "./parse";
import type { PriceService } from "./price/service";
import { normalize, validSymbol, type Quote } from "./price/types";
import type { Holding, Store, User } from "./store";
import type { SendOptions, Telegram, TgUpdate } from "./telegram";

export interface BotDeps {
  store: Store;
  prices: PriceService;
  chains: Registry;
  tg: Telegram;
  http: Http;
  log: Logger;
  maxAlerts: number;
  maxWallets: number;
}

interface Ctx {
  chatId: number;
  user: User;
  text: string;
  args: string[];
  reply(html: string, opts?: SendOptions): Promise<unknown>;
  fail(err: unknown, msg: string): Promise<unknown>;
}

type Handler = (c: Ctx, d: BotDeps) => Promise<unknown>;

const helpText = `<b>Prices</b>
/price btc eth — current prices
/top — top 10 coins by market cap
/chart btc 7d — price chart (1d, 7d, 30d, 90d, 1y)

<b>Price alerts</b>
/alert btc above 100000 — notify when price crosses a level
/alerts — your active alerts
/delalert 12 — delete alert #12

<b>Wallet tracking</b>
/watch eth 0xabc… Vitalik — watch a wallet (label optional)
/wallets — wallets you watch
/unwatch 5 — stop watching #5
/minusd 5 1000 — only notify transfers worth ≥ $1,000
/balance sol &lt;address&gt; — native coin balance
/chains — supported chains

<b>Portfolio</b>
/add btc 0.5 60000 — record a buy (quantity, price)
/sell btc 0.1 70000 — record a sell (price optional)
/remove btc — delete a holding
/portfolio — value and P&amp;L

Numbers accept shortcuts like 100k, 1.5m, $2,500.`;

// Per-isolate caches. Cloudflare reuses isolates between requests, so these
// save database writes and stop floods; losing them is harmless.
const userCache = new Map<number, { user: User; expires: number }>();

export class RateLimiter {
  private readonly buckets = new Map<number, { tokens: number; last: number }>();

  constructor(
    private readonly perSecond: number,
    private readonly burst: number,
  ) {}

  allow(id: number, now: number): boolean {
    if (this.buckets.size > 50_000) this.buckets.clear();
    let b = this.buckets.get(id);
    if (!b) {
      b = { tokens: this.burst, last: now };
      this.buckets.set(id, b);
    }
    b.tokens = Math.min(this.burst, b.tokens + ((now - b.last) / 1000) * this.perSecond);
    b.last = now;
    if (b.tokens < 1) return false;
    b.tokens--;
    return true;
  }
}

const throttle = new RateLimiter(1.5, 6);

const handlers: Record<string, Handler> = {
  start: cmdStart,
  help: (c) => c.reply(helpText),
  price: cmdPrice,
  top: cmdTop,
  chart: cmdChart,
  alert: cmdAlert,
  alerts: cmdAlerts,
  delalert: cmdDelAlert,
  watch: cmdWatch,
  wallets: cmdWallets,
  unwatch: cmdUnwatch,
  minusd: cmdMinUSD,
  balance: cmdBalance,
  chains: cmdChains,
  add: cmdAdd,
  sell: cmdSell,
  remove: cmdRemove,
  portfolio: cmdPortfolio,
};

export async function handleUpdate(update: TgUpdate, d: BotDeps): Promise<void> {
  const m = update.message;
  if (!m || m.text === undefined || !m.from || m.from.is_bot || m.chat.type !== "private") return;
  if (!throttle.allow(m.from.id, Date.now())) return;

  const chatId = m.chat.id;
  const text = m.text.trim();
  const reply = (html: string, opts?: SendOptions) => d.tg.sendMessage(chatId, html, opts);
  const fail = (err: unknown, msg: string) => {
    if (err) d.log.error("command failed", { cmd: text, err });
    return reply("⚠️ " + msg);
  };

  let user: User;
  const cached = userCache.get(m.from.id);
  if (cached && cached.expires > Date.now() && cached.user.chatId === chatId) {
    user = cached.user;
  } else {
    try {
      user = await d.store.upsertUser(m.from.id, chatId, m.from.username ?? "", m.from.first_name ?? "");
    } catch (err) {
      await fail(err, "I couldn't reach my database. Please try again in a moment.");
      return;
    }
    if (userCache.size > 50_000) userCache.clear();
    userCache.set(m.from.id, { user, expires: Date.now() + 15 * 60_000 });
  }

  const [head = "", ...args] = text.split(/\s+/);
  const command = head.startsWith("/") ? head.slice(1).split("@")[0]!.toLowerCase() : "";
  const handler = handlers[command];
  const c: Ctx = { chatId, user, text, args, reply, fail };
  try {
    if (handler) await handler(c, d);
    else await reply("I didn't get that. Try /help to see what I can do.");
  } catch (err) {
    d.log.error("telegram handler error", { cmd: text, err });
  }
}

async function cmdStart(c: Ctx, d: BotDeps) {
  // /start reactivates a chat that had blocked the bot before.
  const u = await d.store.upsertUser(c.user.telegramId, c.chatId, c.user.username, c.user.firstName).catch(() => null);
  if (u) userCache.set(u.telegramId, { user: u, expires: Date.now() + 15 * 60_000 });
  const name = c.user.firstName || "there";
  return c.reply(
    `👋 Hi ${format.escape(name)}! I'm <b>ChainWatch</b>.\n\nI track crypto prices, send price alerts, watch on-chain wallets and keep your portfolio P&amp;L.\n\n${helpText}`,
  );
}

async function cmdPrice(c: Ctx, d: BotDeps) {
  if (c.args.length === 0) return c.reply("Usage: <code>/price btc eth sol</code>");
  const symbols: string[] = [];
  for (const a of c.args.slice(0, 10)) {
    if (!validSymbol(a)) return c.reply(`<code>${format.escape(a)}</code> doesn't look like a ticker symbol.`);
    symbols.push(normalize(a));
  }
  await d.tg.sendChatAction(c.chatId, "typing");
  let quotes: Map<string, Quote>;
  try {
    quotes = await d.prices.quotes(symbols, true);
  } catch (err) {
    return c.fail(err, "Price sources are unavailable right now. Try again shortly.");
  }
  const lines: string[] = [];
  const missing: string[] = [];
  for (const s of symbols) {
    const q = quotes.get(s);
    if (!q) {
      missing.push(s);
      continue;
    }
    let line = `${format.trend(q.change24h)} <b>${s}</b>  ${format.usd(q.price)}`;
    if (q.hasChange) line += `  <i>${format.percent(q.change24h)} 24h</i>`;
    lines.push(line);
  }
  if (missing.length > 0) lines.push("\n❓ Not found: " + format.escape(missing.join(", ")));
  return c.reply(lines.join("\n"));
}

async function cmdTop(c: Ctx, d: BotDeps) {
  await d.tg.sendChatAction(c.chatId, "typing");
  let coins;
  try {
    coins = await d.prices.top(10);
  } catch (err) {
    return c.fail(err, "Couldn't load the market list right now.");
  }
  let out = "🏆 <b>Top 10 by market cap</b>\n";
  for (const coin of coins) {
    out +=
      `\n${coin.rank}. <b>${format.escape(coin.symbol)}</b> ${format.escape(coin.name)}\n` +
      `    ${format.usd(coin.price)} · ${format.trend(coin.change24h)} ${format.percent(coin.change24h)} · MC ${format.compactUSD(coin.marketCap)}`;
  }
  out += "\n\n<i>Data: CoinGecko</i>";
  return c.reply(out);
}

async function cmdChart(c: Ctx, d: BotDeps) {
  if (c.args.length === 0 || !validSymbol(c.args[0]!)) return c.reply("Usage: <code>/chart btc 7d</code> (1d, 7d, 30d, 90d, 1y)");
  const symbol = normalize(c.args[0]!);
  const days = parseDays(c.args[1] ?? "");
  if (days === null) return c.reply("Pick a range: 1d, 7d, 30d, 90d or 1y.");

  await d.tg.sendChatAction(c.chatId, "upload_photo");
  let points;
  try {
    points = await d.prices.history(symbol, days);
  } catch (err) {
    if (err instanceof NotFoundError) return c.reply(`No chart data for <b>${format.escape(symbol)}</b>.`);
    return c.fail(err, "Couldn't load chart data right now.");
  }
  const label = days === 365 ? "1y" : `${days}d`;
  let png: ArrayBuffer;
  try {
    png = await renderChart(d.http, `${symbol} / USD · ${label}`, points, days);
  } catch (err) {
    return c.fail(err, "Couldn't draw the chart.");
  }
  const first = points[0]!.price;
  const last = points[points.length - 1]!.price;
  const change = ((last - first) / first) * 100;
  const caption = `<b>${format.escape(symbol)}</b> ${label}: ${format.usd(first)} → ${format.usd(last)} (${format.trend(change)} ${format.percent(change)})`;
  return d.tg.sendPhoto(c.chatId, png, caption);
}

async function cmdAlert(c: Ctx, d: BotDeps) {
  const usage = "Usage: <code>/alert btc above 100000</code> or <code>/alert eth below 2500</code>";
  if (c.args.length !== 3 || !validSymbol(c.args[0]!)) return c.reply(usage);
  const symbol = normalize(c.args[0]!);
  const dir = parseDirection(c.args[1]!);
  if (!dir) return c.reply(usage);
  const target = parseNumber(c.args[2]!);
  if (target === null) {
    return c.reply("The target price must be a positive number, e.g. <code>100000</code> or <code>100k</code>.");
  }

  let count: number;
  try {
    count = await d.store.countActiveAlerts(c.user.id);
  } catch (err) {
    return c.fail(err, "Couldn't save the alert. Please try again.");
  }
  if (d.maxAlerts > 0 && count >= d.maxAlerts) {
    return c.reply(`You already have ${count} active alerts (the limit). Remove one with /delalert.`);
  }

  let q: Quote;
  try {
    q = await d.prices.quote(symbol, false);
  } catch (err) {
    if (err instanceof NotFoundError) return c.reply(`I can't find a price for <b>${format.escape(symbol)}</b>.`);
    return c.fail(err, "Price sources are unavailable right now. Try again shortly.");
  }
  if (triggered(dir, target, q.price)) {
    return c.reply(
      `<b>${format.escape(symbol)}</b> is already ${dir} ${format.usd(target)} (now ${format.usd(q.price)}). Pick a target on the other side of the current price.`,
    );
  }

  let id: number;
  try {
    id = (await d.store.createAlert(c.user.id, symbol, dir, target)).id;
  } catch (err) {
    return c.fail(err, "Couldn't save the alert. Please try again.");
  }
  const distance = (Math.abs(target - q.price) / q.price) * 100;
  return c.reply(
    `✅ Alert <b>#${id}</b> set: I'll ping you when <b>${format.escape(symbol)}</b> goes ${dir} <b>${format.usd(target)}</b>.\nNow ${format.usd(q.price)} · ${distance.toFixed(2)}% away.`,
  );
}

async function cmdAlerts(c: Ctx, d: BotDeps) {
  let alerts;
  try {
    alerts = await d.store.userAlerts(c.user.id);
  } catch (err) {
    return c.fail(err, "Couldn't load your alerts.");
  }
  if (alerts.length === 0) return c.reply("You have no active alerts. Create one: <code>/alert btc above 100000</code>");
  let out = "⏰ <b>Your price alerts</b>\n";
  const now = Date.now();
  for (const a of alerts) {
    out += `\n<b>#${a.id}</b> ${format.escape(a.symbol)} ${a.direction} ${format.usd(a.target)} · <i>${format.ago(a.createdAt, now)}</i>`;
  }
  out += "\n\nDelete with <code>/delalert &lt;id&gt;</code>";
  return c.reply(out);
}

async function cmdDelAlert(c: Ctx, d: BotDeps) {
  const id = c.args.length === 1 ? parseID(c.args[0]!) : null;
  if (id === null) return c.reply("Usage: <code>/delalert 12</code>");
  let deleted: boolean;
  try {
    deleted = await d.store.deleteAlert(c.user.id, id);
  } catch (err) {
    return c.fail(err, "Couldn't delete the alert.");
  }
  if (!deleted) return c.reply(`No active alert #${id} found.`);
  return c.reply(`🗑 Alert #${id} deleted.`);
}

function chainList(d: BotDeps): string {
  return d.chains
    .all()
    .map((t) => t.id)
    .join(", ");
}

async function cmdChains(c: Ctx, d: BotDeps) {
  let out = "⛓ <b>Supported chains</b>\n";
  for (const t of d.chains.all()) out += `\n<code>${t.id}</code> — ${t.name} (${t.nativeSymbol})`;
  out += "\n\nExample: <code>/watch eth 0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045 Vitalik</code>";
  return c.reply(out);
}

// Replies with the problem and returns null when the chain or address is bad.
async function resolveAddress(c: Ctx, d: BotDeps, chainArg: string, addrArg: string): Promise<[Tracker, string] | null> {
  const tracker = d.chains.get(chainArg);
  if (!tracker) {
    await c.reply(`Unknown chain <code>${format.escape(chainArg)}</code>. Supported: ${chainList(d)}`);
    return null;
  }
  try {
    return [tracker, tracker.normalize(addrArg)];
  } catch {
    await c.reply(`That doesn't look like a valid ${tracker.name} address.`);
    return null;
  }
}

async function cmdWatch(c: Ctx, d: BotDeps) {
  if (c.args.length < 2) return c.reply(`Usage: <code>/watch &lt;chain&gt; &lt;address&gt; [label]</code>\nChains: ${chainList(d)}`);
  const resolved = await resolveAddress(c, d, c.args[0]!, c.args[1]!);
  if (!resolved) return;
  const [tracker, addr] = resolved;
  const label = Array.from(c.args.slice(2).join(" ").trim()).slice(0, 40).join("");

  await d.tg.sendChatAction(c.chatId, "typing");
  let subId: number;
  let created: boolean;
  try {
    const count = await d.store.countSubscriptions(c.user.id);
    if (d.maxWallets > 0 && count >= d.maxWallets) {
      return c.reply(`You're already watching ${count} wallets (the limit). Remove one with /unwatch.`);
    }
    let walletId = await d.store.findWallet(tracker.id, addr);
    if (walletId === null) {
      let cursor: string;
      try {
        cursor = await tracker.bootstrap(addr);
      } catch (err) {
        if (err instanceof InvalidAddressError) return c.reply(`${tracker.name} rejected that address.`);
        return c.fail(err, `Couldn't reach the ${tracker.name} data provider. Try again in a minute.`);
      }
      walletId = await d.store.createWallet(tracker.id, addr, cursor);
    }
    ({ id: subId, created } = await d.store.subscribe(c.user.id, walletId, label));
  } catch (err) {
    return c.fail(err, "Couldn't save that. Please try again.");
  }

  let name = format.escape(format.shortAddr(addr));
  if (label !== "") name = `<b>${format.escape(label)}</b> (${name})`;
  if (!created) return c.reply(`You're already watching ${name} on ${tracker.name} (#${subId}).`);
  return c.reply(
    `👀 Watching ${name} on ${tracker.name} as <b>#${subId}</b>.\n\nI'll message you when new transfers confirm. To skip small ones: <code>/minusd ${subId} 1000</code>`,
  );
}

async function cmdWallets(c: Ctx, d: BotDeps) {
  let subs;
  try {
    subs = await d.store.userSubscriptions(c.user.id);
  } catch (err) {
    return c.fail(err, "Couldn't load your wallets.");
  }
  if (subs.length === 0) {
    return c.reply("You aren't watching any wallets yet.\nTry: <code>/watch eth 0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045 Vitalik</code>");
  }
  let out = "👛 <b>Watched wallets</b>\n";
  for (const s of subs) {
    const t = d.chains.get(s.chain);
    out += `\n<b>#${s.id}</b> ${t ? t.name : s.chain} · <a href="${t ? t.addressURL(s.address) : ""}">${format.escape(format.shortAddr(s.address))}</a>`;
    if (s.label !== "") out += " · " + format.escape(s.label);
    if (s.minUSD > 0) out += " · min " + format.usd(s.minUSD);
  }
  out += "\n\nStop with <code>/unwatch &lt;id&gt;</code>";
  return c.reply(out, { noPreview: true });
}

async function cmdUnwatch(c: Ctx, d: BotDeps) {
  const id = c.args.length === 1 ? parseID(c.args[0]!) : null;
  if (id === null) return c.reply("Usage: <code>/unwatch 5</code> (see /wallets for ids)");
  let deleted: boolean;
  try {
    deleted = await d.store.unsubscribe(c.user.id, id);
  } catch (err) {
    return c.fail(err, "Couldn't update your wallets.");
  }
  if (!deleted) return c.reply(`No watched wallet #${id} found.`);
  return c.reply(`🛑 Stopped watching #${id}.`);
}

async function cmdMinUSD(c: Ctx, d: BotDeps) {
  const usage = "Usage: <code>/minusd 5 1000</code> — only notify transfers worth at least $1,000 (0 = everything)";
  if (c.args.length !== 2) return c.reply(usage);
  const id = parseID(c.args[0]!);
  if (id === null) return c.reply(usage);
  let minUSD = 0;
  if (c.args[1]!.trim() !== "0") {
    const v = parseNumber(c.args[1]!);
    if (v === null) return c.reply(usage);
    minUSD = v;
  }
  let updated: boolean;
  try {
    updated = await d.store.setMinUSD(c.user.id, id, minUSD);
  } catch (err) {
    return c.fail(err, "Couldn't update that wallet.");
  }
  if (!updated) return c.reply(`No watched wallet #${id} found.`);
  if (minUSD === 0) return c.reply(`✅ #${id} will notify every transfer.`);
  return c.reply(`✅ #${id} will only notify transfers worth ≥ ${format.usd(minUSD)}. Unpriced or unverified tokens are skipped.`);
}

async function cmdBalance(c: Ctx, d: BotDeps) {
  if (c.args.length !== 2) return c.reply(`Usage: <code>/balance &lt;chain&gt; &lt;address&gt;</code>\nChains: ${chainList(d)}`);
  const resolved = await resolveAddress(c, d, c.args[0]!, c.args[1]!);
  if (!resolved) return;
  const [tracker, addr] = resolved;
  await d.tg.sendChatAction(c.chatId, "typing");
  let bal;
  try {
    bal = await tracker.balance(addr);
  } catch (err) {
    if (err instanceof InvalidAddressError) return c.reply(`${tracker.name} rejected that address.`);
    return c.fail(err, `Couldn't reach the ${tracker.name} data provider.`);
  }
  let msg = `💰 <b>${tracker.name} balance</b>\n<a href="${tracker.addressURL(addr)}">${format.escape(format.shortAddr(addr))}</a>\n\n<b>${format.amount(bal.amount)} ${bal.symbol}</b>`;
  try {
    const q = await d.prices.quote(bal.symbol, false);
    msg += ` (≈ ${format.usd(bal.amount * q.price)})`;
  } catch {
    // Balance without a USD value is still useful.
  }
  msg += "\n\n<i>Native coin only; token balances aren't included.</i>";
  return c.reply(msg, { noPreview: true });
}

export interface PortfolioLine extends Holding {
  price: number;
  change24h: number;
  known: boolean;
  value: number;
  cost: number;
  pnl: number;
  pnlPct: number;
  weight: number;
}

export interface PortfolioSummary {
  lines: PortfolioLine[];
  value: number;
  cost: number;
  pnl: number;
}

export function summarize(holdings: Holding[], quotes: Map<string, Quote>): PortfolioSummary {
  const s: PortfolioSummary = { lines: [], value: 0, cost: 0, pnl: 0 };
  for (const h of holdings) {
    const l: PortfolioLine = { ...h, price: 0, change24h: 0, known: false, value: 0, cost: h.quantity * h.avgCost, pnl: 0, pnlPct: 0, weight: 0 };
    const q = quotes.get(h.symbol);
    if (q && q.price > 0) {
      l.known = true;
      l.price = q.price;
      l.change24h = q.change24h;
      l.value = h.quantity * q.price;
      l.pnl = l.value - l.cost;
      if (l.cost > 0) l.pnlPct = (l.pnl / l.cost) * 100;
      s.value += l.value;
      s.cost += l.cost;
    }
    s.lines.push(l);
  }
  s.pnl = s.value - s.cost;
  for (const l of s.lines) {
    if (s.value > 0 && l.known) l.weight = (l.value / s.value) * 100;
  }
  return s;
}

async function cmdAdd(c: Ctx, d: BotDeps) {
  const usage = "Usage: <code>/add btc 0.5 60000</code> — symbol, quantity, buy price in USD";
  if (c.args.length !== 3 || !validSymbol(c.args[0]!)) return c.reply(usage);
  const symbol = normalize(c.args[0]!);
  const qty = parseNumber(c.args[1]!);
  const cost = parseNumber(c.args[2]!);
  if (qty === null || cost === null) return c.reply(usage);
  try {
    await d.prices.quote(symbol, false);
  } catch (err) {
    if (err instanceof NotFoundError) return c.reply(`I can't find a price for <b>${format.escape(symbol)}</b>, so I can't track it.`);
  }
  let h: Holding;
  try {
    h = await d.store.addHolding(c.user.id, symbol, qty, cost);
  } catch (err) {
    return c.fail(err, "Couldn't save that holding.");
  }
  return c.reply(
    `✅ Added ${format.amount(qty)} <b>${symbol}</b> @ ${format.usd(cost)}.\nNow holding <b>${format.amount(h.quantity)} ${symbol}</b> · avg cost ${format.usd(h.avgCost)}`,
  );
}

async function cmdSell(c: Ctx, d: BotDeps) {
  const usage = "Usage: <code>/sell btc 0.1 70000</code> — symbol, quantity, optional sell price";
  if ((c.args.length !== 2 && c.args.length !== 3) || !validSymbol(c.args[0]!)) return c.reply(usage);
  const symbol = normalize(c.args[0]!);
  const qty = parseNumber(c.args[1]!);
  if (qty === null) return c.reply(usage);
  let sellPrice = 0;
  if (c.args.length === 3) {
    const p = parseNumber(c.args[2]!);
    if (p === null) return c.reply(usage);
    sellPrice = p;
  }
  let res;
  try {
    res = await d.store.sellHolding(c.user.id, symbol, qty);
  } catch (err) {
    if (err instanceof NotFoundError) return c.reply(`You don't hold any <b>${symbol}</b>.`);
    return c.fail(err, "Couldn't update that holding.");
  }
  let msg = `✅ Sold ${format.amount(res.sold)} <b>${symbol}</b>.`;
  if (sellPrice > 0) {
    const realized = (sellPrice - res.avgCost) * res.sold;
    msg += `\nRealized P&amp;L: <b>${format.signedUSD(realized)}</b> ${format.trend(realized)}`;
  }
  msg += res.remaining > 0 ? `\nRemaining: ${format.amount(res.remaining)} ${symbol}` : "\nPosition closed.";
  return c.reply(msg);
}

async function cmdRemove(c: Ctx, d: BotDeps) {
  if (c.args.length !== 1 || !validSymbol(c.args[0]!)) return c.reply("Usage: <code>/remove btc</code>");
  const symbol = normalize(c.args[0]!);
  let removed: boolean;
  try {
    removed = await d.store.removeHolding(c.user.id, symbol);
  } catch (err) {
    return c.fail(err, "Couldn't remove that holding.");
  }
  if (!removed) return c.reply(`You don't hold any <b>${symbol}</b>.`);
  return c.reply(`🗑 Removed <b>${symbol}</b> from your portfolio.`);
}

async function cmdPortfolio(c: Ctx, d: BotDeps) {
  let holdings: Holding[];
  try {
    holdings = await d.store.holdings(c.user.id);
  } catch (err) {
    return c.fail(err, "Couldn't load your portfolio.");
  }
  if (holdings.length === 0) return c.reply("Your portfolio is empty. Add a position: <code>/add btc 0.5 60000</code>");
  await d.tg.sendChatAction(c.chatId, "typing");
  let quotes: Map<string, Quote>;
  try {
    quotes = await d.prices.quotes(
      holdings.map((h) => h.symbol),
      true,
    );
  } catch (err) {
    return c.fail(err, "Price sources are unavailable right now.");
  }
  const sum = summarize(holdings, quotes);
  let out = "💼 <b>Portfolio</b>\n";
  for (const l of sum.lines) {
    out += `\n<b>${l.symbol}</b> · ${format.amount(l.quantity)} @ ${format.usd(l.avgCost)}`;
    if (!l.known) {
      out += "\n   price unavailable";
      continue;
    }
    out += `\n   ${format.usd(l.value)} · ${format.trend(l.pnl)} ${format.signedUSD(l.pnl)} (${format.percent(l.pnlPct)}) · ${l.weight.toFixed(1)}%`;
  }
  const pct = sum.cost > 0 ? (sum.pnl / sum.cost) * 100 : 0;
  out += `\n\n<b>Total</b> ${format.usd(sum.value)}\nCost ${format.usd(sum.cost)} · P&amp;L <b>${format.signedUSD(sum.pnl)}</b> (${format.percent(pct)}) ${format.trend(sum.pnl)}`;
  return c.reply(out);
}
