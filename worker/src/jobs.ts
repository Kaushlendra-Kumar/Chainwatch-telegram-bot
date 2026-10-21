import type { ChainEvent, Tracker } from "./chain/chain";
import { BudgetExhaustedError, RecipientGoneError } from "./errors";
import * as format from "./format";
import type { Budget } from "./http";
import type { Logger } from "./log";
import { normalize, type Quote } from "./price/types";
import type { Alert, DueWallet, StoredEvent, Subscription } from "./store";

export interface Notifier {
  notify(chatId: number, html: string): Promise<void>;
}

export interface PriceSource {
  quotes(symbols: string[], withChange: boolean): Promise<Map<string, Quote>>;
}

export interface ChainLookup {
  get(id: string): Tracker | undefined;
}

export interface AlertStore {
  activeAlerts(): Promise<Alert[]>;
  claimAlert(alertId: number, price: number): Promise<boolean>;
  releaseAlert(alertId: number): Promise<void>;
  deactivateChat(chatId: number): Promise<void>;
}

export interface WalletStore {
  walletsDue(limit: number, minAgeSec: number): Promise<DueWallet[]>;
  touchWallet(walletId: number): Promise<void>;
  saveEvents(walletId: number, events: ChainEvent[], cursor: string): Promise<number>;
  claimPendingEvents(sinceSec: number, limit: number): Promise<StoredEvent[]>;
  releaseEvents(ids: number[]): Promise<void>;
  subscriptionsForWallets(walletIds: number[]): Promise<Subscription[]>;
  deactivateChat(chatId: number): Promise<void>;
}

export function triggered(direction: string, target: number, current: number): boolean {
  if (current <= 0) return false;
  if (direction === "above") return current >= target;
  if (direction === "below") return current <= target;
  return false;
}

export function alertMessage(a: Alert, current: number, now: number): string {
  const arrow = a.direction === "below" ? "📉" : "📈";
  return (
    `🚨 <b>Price alert · ${format.escape(a.symbol)}</b>\n\n` +
    `${arrow} ${format.escape(a.symbol)} is now <b>${a.direction}</b> ${format.usd(a.target)}\n\n` +
    `Current price: <b>${format.usd(current)}</b>\nAlert #${a.id} · set ${format.ago(a.createdAt, now)}\n\n` +
    `<i>This alert is now complete. Create a new one with /alert.</i>`
  );
}

// Checks every active alert against current prices and fires the ones that
// crossed. Returns how many were delivered.
export async function runAlerts(
  st: AlertStore,
  prices: PriceSource,
  notifier: Notifier,
  budget: Budget,
  log: Logger,
  now = () => Date.now(),
): Promise<number> {
  const alerts = await st.activeAlerts();
  if (alerts.length === 0) return 0;
  let quotes: Map<string, Quote>;
  try {
    quotes = await prices.quotes(
      alerts.map((a) => a.symbol),
      false,
    );
  } catch (err) {
    log.warn("fetch prices", { err });
    return 0;
  }
  let fired = 0;
  const gone = new Set<number>();
  for (const a of alerts) {
    const q = quotes.get(a.symbol);
    if (!q || !triggered(a.direction, a.target, q.price) || gone.has(a.chatId)) continue;
    if (budget.remaining < 1) break;
    if (!(await st.claimAlert(a.id, q.price))) continue;
    try {
      await notifier.notify(a.chatId, alertMessage(a, q.price, now()));
      fired++;
      log.info("alert fired", { alert: a.id, symbol: a.symbol, price: q.price });
    } catch (err) {
      if (err instanceof RecipientGoneError) {
        gone.add(a.chatId);
        await st.deactivateChat(a.chatId).catch(() => undefined);
        continue;
      }
      log.warn("send alert failed, will retry", { alert: a.id, err });
      await st.releaseAlert(a.id).catch((rerr) => log.error("release alert", { alert: a.id, err: rerr }));
    }
  }
  return fired;
}

// Polls the least recently polled wallets until the run's request budget,
// minus `reserve` kept for notifications, is used up. The rest wait for the
// next run.
export async function pollWallets(
  st: WalletStore,
  chains: ChainLookup,
  budget: Budget,
  log: Logger,
  reserve: number,
  minAgeSec = 50,
): Promise<number> {
  const due = await st.walletsDue(30, minAgeSec);
  let inserted = 0;
  for (const w of due) {
    const tracker = chains.get(w.chain);
    if (!tracker) continue;
    if (budget.remaining - reserve < tracker.pollCost) break;
    try {
      const { events, cursor } = await tracker.poll(w.address, w.cursor);
      inserted += await st.saveEvents(w.id, events, cursor);
    } catch (err) {
      if (err instanceof BudgetExhaustedError) break;
      log.warn("poll wallet", { chain: w.chain, wallet: w.id, err });
      await st.touchWallet(w.id).catch(() => undefined);
    }
  }
  if (inserted > 0) log.info("new wallet events", { count: inserted });
  return inserted;
}

export interface EventView extends ChainEvent {
  usd: number;
  known: boolean;
}

export function value(e: ChainEvent, quotes: Map<string, Quote>): EventView {
  const v: EventView = { ...e, usd: 0, known: false };
  if (!e.verified) return v;
  const q = quotes.get(normalize(e.symbol));
  if (q && q.price > 0) {
    v.usd = e.amount * q.price;
    v.known = true;
  }
  return v;
}

export function passes(minUSD: number, v: EventView): boolean {
  if (minUSD <= 0) return true;
  return v.known && v.usd >= minUSD;
}

const claimPage = 50;

// Sends pending wallet events to their subscribers. Events for wallets the
// budget can't cover are put back for the next run.
export async function dispatchEvents(
  st: WalletStore,
  chains: ChainLookup,
  prices: PriceSource,
  notifier: Notifier,
  budget: Budget,
  log: Logger,
  batchLimit: number,
): Promise<void> {
  for (let round = 0; round < 5; round++) {
    if (budget.remaining < 1) return;
    const events = await st.claimPendingEvents(Math.floor(Date.now() / 1000) - 6 * 3600, claimPage);
    if (events.length === 0) return;
    const finished = await deliver(st, chains, prices, notifier, budget, log, batchLimit, events);
    if (!finished || events.length < claimPage) return;
  }
}

async function deliver(
  st: WalletStore,
  chains: ChainLookup,
  prices: PriceSource,
  notifier: Notifier,
  budget: Budget,
  log: Logger,
  batchLimit: number,
  events: StoredEvent[],
): Promise<boolean> {
  const byWallet = new Map<number, StoredEvent[]>();
  for (const e of events) {
    const list = byWallet.get(e.walletId) ?? [];
    list.push(e);
    byWallet.set(e.walletId, list);
  }
  const walletIds = [...byWallet.keys()].sort((a, b) => a - b);
  const subs = await st.subscriptionsForWallets(walletIds);

  let quotes = new Map<string, Quote>();
  const symbols = events.filter((e) => e.verified).map((e) => e.symbol);
  if (symbols.length > 0) {
    try {
      quotes = await prices.quotes(symbols, false);
    } catch {
      // Notifications still go out, just without USD values.
    }
  }

  const gone = new Set<number>();
  for (let i = 0; i < walletIds.length; i++) {
    const walletId = walletIds[i]!;
    const walletSubs = subs.filter((s) => s.walletId === walletId);
    const tracker = walletSubs[0] ? chains.get(walletSubs[0].chain) : undefined;
    if (!tracker) continue;
    const views = byWallet.get(walletId)!.map((e) => value(e, quotes));
    const messages = walletSubs
      .filter((s) => !gone.has(s.chatId))
      .map((sub) => ({ sub, items: views.filter((v) => passes(sub.minUSD, v)) }))
      .filter((m) => m.items.length > 0);
    if (messages.length > budget.remaining) {
      const ids = walletIds.slice(i).flatMap((id) => byWallet.get(id)!.map((e) => e.id));
      await st.releaseEvents(ids);
      return false;
    }
    for (const { sub, items } of messages) {
      try {
        await notifier.notify(sub.chatId, walletMessage(tracker, sub, items, batchLimit));
      } catch (err) {
        if (err instanceof RecipientGoneError) {
          gone.add(sub.chatId);
          await st.deactivateChat(sub.chatId).catch(() => undefined);
        } else {
          log.warn("send wallet notification", { subscription: sub.id, err });
        }
      }
    }
  }
  return true;
}

export function walletMessage(
  tracker: Pick<Tracker, "name" | "txURL" | "addressURL">,
  sub: Pick<Subscription, "id" | "address" | "label">,
  items: EventView[],
  limit: number,
): string {
  const title = sub.label || format.shortAddr(sub.address);
  let out =
    `🔔 <b>Wallet activity · ${format.escape(title)}</b>\n` +
    `${tracker.name} · <a href="${tracker.addressURL(sub.address)}">${format.escape(format.shortAddr(sub.address))}</a>\n`;
  const shown = limit > 0 && items.length > limit ? items.slice(items.length - limit) : items;
  for (const v of shown) {
    out += "\n" + eventLine(tracker, v);
  }
  const hidden = items.length - shown.length;
  if (hidden > 0) {
    out += `\n…and ${hidden} earlier transfer(s). Use /minusd ${sub.id} &lt;amount&gt; to filter small ones.`;
  }
  return out;
}

function eventLine(tracker: Pick<Tracker, "txURL">, v: EventView): string {
  let [icon, verb, prep] = ["📥", "Received", "from"];
  if (v.direction === "out") [icon, verb, prep] = ["📤", "Sent", "to"];
  else if (v.direction === "self") [icon, verb, prep] = ["🔁", "Self-transfer", ""];
  let line = `${icon} <b>${verb} ${format.amount(v.amount)} ${format.escape(v.symbol)}</b>`;
  if (v.known) line += ` (≈ ${format.usd(v.usd)})`;
  if (!v.verified) line += " ⚠️ <i>unverified token</i>";
  line += "\n";
  if (prep !== "" && v.counterparty !== "") {
    line += `${prep} <code>${format.escape(format.shortAddr(v.counterparty))}</code> · `;
  }
  line += `<a href="${tracker.txURL(v.txHash)}">view tx</a>`;
  return line;
}
