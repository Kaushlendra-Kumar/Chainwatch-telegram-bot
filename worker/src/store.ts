import type { ChainEvent, Direction } from "./chain/chain";
import { NotFoundError } from "./errors";

// The subset of Cloudflare's D1 API the store uses. Declared here so tests can
// run against node:sqlite without the Workers runtime types.
export interface Statement {
  bind(...values: unknown[]): Statement;
  first<T = Record<string, unknown>>(): Promise<T | null>;
  all<T = Record<string, unknown>>(): Promise<{ results: T[] }>;
  run(): Promise<{ meta: { changes: number } }>;
}

export interface Database {
  prepare(sql: string): Statement;
  batch(statements: Statement[]): Promise<{ meta: { changes: number } }[]>;
}

export interface User {
  id: number;
  telegramId: number;
  chatId: number;
  username: string;
  firstName: string;
}

// Timestamps on these types are unix milliseconds.
export interface Alert {
  id: number;
  userId: number;
  chatId: number;
  symbol: string;
  direction: "above" | "below";
  target: number;
  createdAt: number;
}

export interface Subscription {
  id: number;
  userId: number;
  chatId: number;
  walletId: number;
  chain: string;
  address: string;
  cursor: string;
  label: string;
  minUSD: number;
  createdAt: number;
}

export interface DueWallet {
  id: number;
  chain: string;
  address: string;
  cursor: string;
}

export interface StoredEvent extends ChainEvent {
  id: number;
  walletId: number;
}

export interface Holding {
  symbol: string;
  quantity: number;
  avgCost: number;
}

export interface SellResult {
  sold: number;
  avgCost: number;
  remaining: number;
}

export interface CleanupResult {
  events: number;
  alerts: number;
  wallets: number;
}

const nowSec = () => Math.floor(Date.now() / 1000);

type AlertRow = { id: number; user_id: number; chat_id: number; symbol: string; direction: string; target: number; created_at: number };

const toAlert = (r: AlertRow): Alert => ({
  id: r.id,
  userId: r.user_id,
  chatId: r.chat_id,
  symbol: r.symbol,
  direction: r.direction as Alert["direction"],
  target: r.target,
  createdAt: r.created_at * 1000,
});

const subscriptionColumns = `
  s.id, s.user_id, u.chat_id, s.wallet_id, w.chain, w.address, w.poll_cursor, s.label, s.min_usd, s.created_at`;

type SubRow = {
  id: number;
  user_id: number;
  chat_id: number;
  wallet_id: number;
  chain: string;
  address: string;
  poll_cursor: string;
  label: string;
  min_usd: number;
  created_at: number;
};

const toSubscription = (r: SubRow): Subscription => ({
  id: r.id,
  userId: r.user_id,
  chatId: r.chat_id,
  walletId: r.wallet_id,
  chain: r.chain,
  address: r.address,
  cursor: r.poll_cursor,
  label: r.label,
  minUSD: r.min_usd,
  createdAt: r.created_at * 1000,
});

type EventRow = {
  id: number;
  wallet_id: number;
  tx_hash: string;
  event_key: string;
  direction: string;
  counterparty: string;
  amount: number;
  symbol: string;
  token: string;
  verified: number;
  block_number: number;
  occurred_at: number;
};

const placeholders = (n: number) => Array(n).fill("?").join(", ");

export class Store {
  constructor(private readonly db: Database) {}

  async ping(): Promise<void> {
    await this.db.prepare("SELECT 1").first();
  }

  async upsertUser(telegramId: number, chatId: number, username: string, firstName: string): Promise<User> {
    const row = await this.db
      .prepare(
        `INSERT INTO users (telegram_id, chat_id, username, first_name)
         VALUES (?, ?, ?, ?)
         ON CONFLICT (telegram_id) DO UPDATE
         SET chat_id = excluded.chat_id,
             username = excluded.username,
             first_name = excluded.first_name,
             active = 1,
             updated_at = unixepoch()
         RETURNING id`,
      )
      .bind(telegramId, chatId, username, firstName)
      .first<{ id: number }>();
    return { id: row!.id, telegramId, chatId, username, firstName };
  }

  async deactivateChat(chatId: number): Promise<void> {
    await this.db.prepare(`UPDATE users SET active = 0, updated_at = unixepoch() WHERE chat_id = ?`).bind(chatId).run();
  }

  async countActiveAlerts(userId: number): Promise<number> {
    const row = await this.db
      .prepare(`SELECT count(*) AS n FROM price_alerts WHERE user_id = ? AND triggered_at IS NULL`)
      .bind(userId)
      .first<{ n: number }>();
    return row?.n ?? 0;
  }

  async createAlert(userId: number, symbol: string, direction: Alert["direction"], target: number): Promise<Alert> {
    const row = await this.db
      .prepare(
        `INSERT INTO price_alerts (user_id, symbol, direction, target)
         VALUES (?, ?, ?, ?)
         RETURNING id, created_at`,
      )
      .bind(userId, symbol, direction, target)
      .first<{ id: number; created_at: number }>();
    return { id: row!.id, userId, chatId: 0, symbol, direction, target, createdAt: row!.created_at * 1000 };
  }

  async userAlerts(userId: number): Promise<Alert[]> {
    const { results } = await this.db
      .prepare(
        `SELECT a.id, a.user_id, 0 AS chat_id, a.symbol, a.direction, a.target, a.created_at
         FROM price_alerts a
         WHERE a.user_id = ? AND a.triggered_at IS NULL
         ORDER BY a.symbol, a.target`,
      )
      .bind(userId)
      .all<AlertRow>();
    return results.map(toAlert);
  }

  async deleteAlert(userId: number, alertId: number): Promise<boolean> {
    const res = await this.db
      .prepare(`DELETE FROM price_alerts WHERE id = ? AND user_id = ? AND triggered_at IS NULL`)
      .bind(alertId, userId)
      .run();
    return res.meta.changes > 0;
  }

  async activeAlerts(): Promise<Alert[]> {
    const { results } = await this.db
      .prepare(
        `SELECT a.id, a.user_id, u.chat_id, a.symbol, a.direction, a.target, a.created_at
         FROM price_alerts a
         JOIN users u ON u.id = a.user_id
         WHERE a.triggered_at IS NULL AND u.active = 1`,
      )
      .all<AlertRow>();
    return results.map(toAlert);
  }

  async claimAlert(alertId: number, price: number): Promise<boolean> {
    const res = await this.db
      .prepare(
        `UPDATE price_alerts
         SET triggered_at = unixepoch(), triggered_price = ?
         WHERE id = ? AND triggered_at IS NULL`,
      )
      .bind(price, alertId)
      .run();
    return res.meta.changes > 0;
  }

  async releaseAlert(alertId: number): Promise<void> {
    await this.db.prepare(`UPDATE price_alerts SET triggered_at = NULL, triggered_price = NULL WHERE id = ?`).bind(alertId).run();
  }

  async findWallet(chain: string, address: string): Promise<number | null> {
    const row = await this.db.prepare(`SELECT id FROM wallets WHERE chain = ? AND address = ?`).bind(chain, address).first<{ id: number }>();
    return row?.id ?? null;
  }

  async createWallet(chain: string, address: string, cursor: string): Promise<number> {
    const row = await this.db
      .prepare(
        `INSERT INTO wallets (chain, address, poll_cursor, last_polled_at)
         VALUES (?, ?, ?, unixepoch())
         ON CONFLICT (chain, address) DO UPDATE SET chain = excluded.chain
         RETURNING id`,
      )
      .bind(chain, address, cursor)
      .first<{ id: number }>();
    return row!.id;
  }

  async countSubscriptions(userId: number): Promise<number> {
    const row = await this.db.prepare(`SELECT count(*) AS n FROM wallet_subscriptions WHERE user_id = ?`).bind(userId).first<{ n: number }>();
    return row?.n ?? 0;
  }

  async subscribe(userId: number, walletId: number, label: string): Promise<{ id: number; created: boolean }> {
    const inserted = await this.db
      .prepare(
        `INSERT INTO wallet_subscriptions (user_id, wallet_id, label)
         VALUES (?, ?, ?)
         ON CONFLICT (user_id, wallet_id) DO NOTHING
         RETURNING id`,
      )
      .bind(userId, walletId, label)
      .first<{ id: number }>();
    if (inserted) return { id: inserted.id, created: true };
    if (label !== "") {
      await this.db.prepare(`UPDATE wallet_subscriptions SET label = ? WHERE user_id = ? AND wallet_id = ?`).bind(label, userId, walletId).run();
    }
    const row = await this.db
      .prepare(`SELECT id FROM wallet_subscriptions WHERE user_id = ? AND wallet_id = ?`)
      .bind(userId, walletId)
      .first<{ id: number }>();
    return { id: row!.id, created: false };
  }

  async userSubscriptions(userId: number): Promise<Subscription[]> {
    const { results } = await this.db
      .prepare(
        `SELECT ${subscriptionColumns}
         FROM wallet_subscriptions s
         JOIN wallets w ON w.id = s.wallet_id
         JOIN users u ON u.id = s.user_id
         WHERE s.user_id = ?
         ORDER BY s.id`,
      )
      .bind(userId)
      .all<SubRow>();
    return results.map(toSubscription);
  }

  async subscriptionsForWallets(walletIds: number[]): Promise<Subscription[]> {
    if (walletIds.length === 0) return [];
    const { results } = await this.db
      .prepare(
        `SELECT ${subscriptionColumns}
         FROM wallet_subscriptions s
         JOIN wallets w ON w.id = s.wallet_id
         JOIN users u ON u.id = s.user_id
         WHERE s.wallet_id IN (${placeholders(walletIds.length)}) AND u.active = 1
         ORDER BY s.wallet_id, s.id`,
      )
      .bind(...walletIds)
      .all<SubRow>();
    return results.map(toSubscription);
  }

  async unsubscribe(userId: number, subId: number): Promise<boolean> {
    const res = await this.db.prepare(`DELETE FROM wallet_subscriptions WHERE id = ? AND user_id = ?`).bind(subId, userId).run();
    return res.meta.changes > 0;
  }

  async setMinUSD(userId: number, subId: number, minUSD: number): Promise<boolean> {
    const res = await this.db
      .prepare(`UPDATE wallet_subscriptions SET min_usd = ? WHERE id = ? AND user_id = ?`)
      .bind(minUSD, subId, userId)
      .run();
    return res.meta.changes > 0;
  }

  // Wallets with at least one active subscriber, least recently polled first.
  async walletsDue(limit: number, minAgeSec: number): Promise<DueWallet[]> {
    const { results } = await this.db
      .prepare(
        `SELECT w.id, w.chain, w.address, w.poll_cursor AS cursor
         FROM wallets w
         WHERE w.last_polled_at <= ?
           AND EXISTS (
             SELECT 1 FROM wallet_subscriptions s
             JOIN users u ON u.id = s.user_id
             WHERE s.wallet_id = w.id AND u.active = 1
           )
         ORDER BY w.last_polled_at, w.id
         LIMIT ?`,
      )
      .bind(nowSec() - minAgeSec, limit)
      .all<DueWallet>();
    return results;
  }

  async touchWallet(walletId: number): Promise<void> {
    await this.db.prepare(`UPDATE wallets SET last_polled_at = unixepoch() WHERE id = ?`).bind(walletId).run();
  }

  // Inserts new events and moves the cursor in one transaction.
  async saveEvents(walletId: number, events: ChainEvent[], cursor: string): Promise<number> {
    const insert = this.db.prepare(
      `INSERT INTO wallet_events
         (wallet_id, tx_hash, event_key, direction, counterparty, amount, symbol, token, verified, block_number, occurred_at)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
       ON CONFLICT (wallet_id, tx_hash, event_key) DO NOTHING`,
    );
    const statements = events.map((e) =>
      insert.bind(
        walletId,
        e.txHash,
        e.key,
        e.direction,
        e.counterparty,
        e.amount,
        e.symbol,
        e.token,
        e.verified ? 1 : 0,
        e.block,
        e.time > 0 ? e.time : nowSec(),
      ),
    );
    statements.push(this.db.prepare(`UPDATE wallets SET poll_cursor = ?, last_polled_at = unixepoch() WHERE id = ?`).bind(cursor, walletId));
    const results = await this.db.batch(statements);
    let inserted = 0;
    for (let i = 0; i < events.length; i++) inserted += results[i]!.meta.changes;
    return inserted;
  }

  // Marks up to `limit` pending events as notified and returns them, oldest first.
  async claimPendingEvents(sinceSec: number, limit: number): Promise<StoredEvent[]> {
    const { results } = await this.db
      .prepare(
        `UPDATE wallet_events
         SET notified_at = unixepoch()
         WHERE id IN (
           SELECT id FROM wallet_events
           WHERE notified_at IS NULL AND created_at >= ?
           ORDER BY id
           LIMIT ?
         )
         RETURNING id, wallet_id, tx_hash, event_key, direction, counterparty, amount, symbol, token, verified, block_number, occurred_at`,
      )
      .bind(sinceSec, limit)
      .all<EventRow>();
    return results
      .map((r) => ({
        id: r.id,
        walletId: r.wallet_id,
        txHash: r.tx_hash,
        key: r.event_key,
        direction: r.direction as Direction,
        counterparty: r.counterparty,
        amount: r.amount,
        symbol: r.symbol,
        token: r.token,
        verified: r.verified === 1,
        block: r.block_number,
        time: r.occurred_at,
      }))
      .sort((a, b) => a.id - b.id);
  }

  async releaseEvents(ids: number[]): Promise<void> {
    for (let i = 0; i < ids.length; i += 90) {
      const chunk = ids.slice(i, i + 90);
      await this.db
        .prepare(`UPDATE wallet_events SET notified_at = NULL WHERE id IN (${placeholders(chunk.length)})`)
        .bind(...chunk)
        .run();
    }
  }

  async addHolding(userId: number, symbol: string, qty: number, price: number): Promise<Holding> {
    const row = await this.db
      .prepare(
        `INSERT INTO holdings (user_id, symbol, quantity, avg_cost)
         VALUES (?, ?, ?, ?)
         ON CONFLICT (user_id, symbol) DO UPDATE
         SET avg_cost = (quantity * avg_cost + excluded.quantity * excluded.avg_cost) / (quantity + excluded.quantity),
             quantity = quantity + excluded.quantity,
             updated_at = unixepoch()
         RETURNING quantity, avg_cost`,
      )
      .bind(userId, symbol, qty, price)
      .first<{ quantity: number; avg_cost: number }>();
    return { symbol, quantity: row!.quantity, avgCost: row!.avg_cost };
  }

  // D1 has no interactive transactions, so the write only applies if the
  // quantity is still the one that was read.
  async sellHolding(userId: number, symbol: string, qty: number): Promise<SellResult> {
    for (let attempt = 0; attempt < 3; attempt++) {
      const row = await this.db
        .prepare(`SELECT quantity, avg_cost FROM holdings WHERE user_id = ? AND symbol = ?`)
        .bind(userId, symbol)
        .first<{ quantity: number; avg_cost: number }>();
      if (!row) throw new NotFoundError();
      if (qty >= row.quantity * (1 - 1e-9)) {
        const res = await this.db
          .prepare(`DELETE FROM holdings WHERE user_id = ? AND symbol = ? AND quantity = ?`)
          .bind(userId, symbol, row.quantity)
          .run();
        if (res.meta.changes > 0) return { sold: row.quantity, avgCost: row.avg_cost, remaining: 0 };
        continue;
      }
      const remaining = row.quantity - qty;
      const res = await this.db
        .prepare(`UPDATE holdings SET quantity = ?, updated_at = unixepoch() WHERE user_id = ? AND symbol = ? AND quantity = ?`)
        .bind(remaining, userId, symbol, row.quantity)
        .run();
      if (res.meta.changes > 0) return { sold: qty, avgCost: row.avg_cost, remaining };
    }
    throw new Error("holding changed while selling, try again");
  }

  async removeHolding(userId: number, symbol: string): Promise<boolean> {
    const res = await this.db.prepare(`DELETE FROM holdings WHERE user_id = ? AND symbol = ?`).bind(userId, symbol).run();
    return res.meta.changes > 0;
  }

  async holdings(userId: number): Promise<Holding[]> {
    const { results } = await this.db
      .prepare(
        `SELECT symbol, quantity, avg_cost FROM holdings
         WHERE user_id = ?
         ORDER BY quantity * avg_cost DESC`,
      )
      .bind(userId)
      .all<{ symbol: string; quantity: number; avg_cost: number }>();
    return results.map((r) => ({ symbol: r.symbol, quantity: r.quantity, avgCost: r.avg_cost }));
  }

  async cleanup(retentionSec: number): Promise<CleanupResult> {
    const cutoff = nowSec() - retentionSec;
    const [events, alerts, wallets] = await this.db.batch([
      this.db.prepare(`DELETE FROM wallet_events WHERE created_at < ?`).bind(cutoff),
      this.db.prepare(`DELETE FROM price_alerts WHERE triggered_at IS NOT NULL AND triggered_at < ?`).bind(cutoff),
      this.db.prepare(
        `DELETE FROM wallets
         WHERE created_at < unixepoch() - 3600
           AND NOT EXISTS (SELECT 1 FROM wallet_subscriptions s WHERE s.wallet_id = wallets.id)`,
      ),
    ]);
    return { events: events!.meta.changes, alerts: alerts!.meta.changes, wallets: wallets!.meta.changes };
  }
}
