import { describe, expect, it } from "vitest";
import type { ChainEvent } from "../src/chain/chain";
import { NotFoundError } from "../src/errors";
import { Store } from "../src/store";
import { FakeD1 } from "./d1";

function setup() {
  const d1 = new FakeD1();
  return { d1, s: new Store(d1) };
}

describe("store", () => {
  it("upserts users and handles alerts", async () => {
    const { s } = setup();
    const u = await s.upsertUser(1001, 1001, "alice", "Alice");
    const u2 = await s.upsertUser(1001, 1001, "alice2", "Alice");
    expect(u2.id).toBe(u.id);

    const a = await s.createAlert(u.id, "BTC", "above", 100000);
    const active = await s.activeAlerts();
    expect(active).toHaveLength(1);
    expect(active[0]!.chatId).toBe(1001);
    expect(await s.claimAlert(a.id, 100500)).toBe(true);
    expect(await s.claimAlert(a.id, 100500)).toBe(false);
    await s.releaseAlert(a.id);
    expect(await s.countActiveAlerts(u.id)).toBe(1);
    expect(await s.userAlerts(u.id)).toHaveLength(1);
    expect(await s.deleteAlert(u.id, a.id)).toBe(true);

    await s.deactivateChat(1001);
    await s.createAlert(u.id, "ETH", "below", 1000);
    expect(await s.activeAlerts()).toHaveLength(0);
    await s.upsertUser(1001, 1001, "alice", "Alice");
    expect(await s.activeAlerts()).toHaveLength(1);
  });

  it("tracks wallets, events and subscriptions", async () => {
    const { d1, s } = setup();
    const u = await s.upsertUser(1, 1, "", "A");
    const wid = await s.createWallet("eth", "0xabc", "100");
    expect(await s.createWallet("eth", "0xabc", "999")).toBe(wid);
    expect(await s.findWallet("eth", "0xabc")).toBe(wid);
    expect(await s.findWallet("eth", "0xnope")).toBeNull();

    const sub = await s.subscribe(u.id, wid, "Whale");
    expect(sub.created).toBe(true);
    const again = await s.subscribe(u.id, wid, "");
    expect(again).toEqual({ id: sub.id, created: false });
    const subs = await s.userSubscriptions(u.id);
    expect(subs[0]!.label).toBe("Whale");
    expect(subs[0]!.cursor).toBe("100");
    expect(await s.setMinUSD(u.id, sub.id, 2500)).toBe(true);

    // New wallets wait one poll interval; make this one due now.
    d1.db.exec("UPDATE wallets SET last_polled_at = 0");
    expect((await s.walletsDue(10, 50)).map((w) => w.id)).toEqual([wid]);

    const events: ChainEvent[] = [
      { txHash: "0x1", key: "native", direction: "in", counterparty: "", amount: 1.5, symbol: "ETH", token: "", verified: true, block: 101, time: 1700000000 },
      { txHash: "0x2", key: "erc20:x", direction: "out", counterparty: "", amount: 50, symbol: "USDC", token: "0xa0b8", verified: true, block: 102, time: 0 },
    ];
    expect(await s.saveEvents(wid, events, "102")).toBe(2);
    expect(await s.saveEvents(wid, events, "102")).toBe(0);
    expect(await s.walletsDue(10, 50)).toHaveLength(0);

    const forWallet = await s.subscriptionsForWallets([wid]);
    expect(forWallet[0]!.cursor).toBe("102");
    expect(forWallet[0]!.minUSD).toBe(2500);

    const since = Math.floor(Date.now() / 1000) - 3600;
    const claimed = await s.claimPendingEvents(since, 100);
    expect(claimed.map((e) => e.txHash)).toEqual(["0x1", "0x2"]);
    expect(claimed[1]!.direction).toBe("out");
    expect(claimed[0]!.verified).toBe(true);
    expect(await s.claimPendingEvents(since, 100)).toHaveLength(0);
    await s.releaseEvents([claimed[0]!.id]);
    expect(await s.claimPendingEvents(since, 100)).toHaveLength(1);

    expect(await s.unsubscribe(u.id, sub.id)).toBe(true);
    d1.db.exec("UPDATE wallets SET created_at = unixepoch() - 7200");
    const res = await s.cleanup(86400);
    expect(res.wallets).toBe(1);
  });

  it("averages, sells and removes holdings", async () => {
    const { s } = setup();
    const u = await s.upsertUser(2, 2, "", "B");
    await s.addHolding(u.id, "BTC", 1, 60000);
    const h = await s.addHolding(u.id, "BTC", 1, 40000);
    expect(h).toEqual({ symbol: "BTC", quantity: 2, avgCost: 50000 });

    const partial = await s.sellHolding(u.id, "BTC", 0.5);
    expect(partial.sold).toBe(0.5);
    expect(partial.remaining).toBeCloseTo(1.5, 12);
    expect(partial.avgCost).toBe(50000);
    const full = await s.sellHolding(u.id, "BTC", 10);
    expect(full).toEqual({ sold: 1.5, avgCost: 50000, remaining: 0 });
    await expect(s.sellHolding(u.id, "BTC", 1)).rejects.toBeInstanceOf(NotFoundError);

    await s.addHolding(u.id, "ETH", 3, 2000);
    expect((await s.holdings(u.id)).map((x) => x.symbol)).toEqual(["ETH"]);
    expect(await s.removeHolding(u.id, "ETH")).toBe(true);
  });
});
