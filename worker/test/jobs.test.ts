import { describe, expect, it } from "vitest";
import type { ChainEvent, Tracker } from "../src/chain/chain";
import { RecipientGoneError } from "../src/errors";
import { Budget } from "../src/http";
import { dispatchEvents, passes, pollWallets, runAlerts, triggered, value, walletMessage, type Notifier } from "../src/jobs";
import { quietLogger } from "../src/log";
import type { Quote } from "../src/price/types";
import { Store, type Alert } from "../src/store";
import { FakeD1 } from "./d1";

const q = (price: number): Quote => ({ symbol: "", price, change24h: 0, hasChange: false, source: "t" });
const fakePrices = (prices: Record<string, number>) => ({
  async quotes(symbols: string[]) {
    return new Map(symbols.filter((s) => s in prices).map((s) => [s, q(prices[s]!)] as const));
  },
});

class FakeNotifier implements Notifier {
  sent: { chatId: number; html: string }[] = [];
  constructor(private readonly errs: Record<number, Error> = {}) {}
  async notify(chatId: number, html: string) {
    const err = this.errs[chatId];
    if (err) throw err;
    this.sent.push({ chatId, html });
  }
}

const fakeTracker: Tracker = {
  id: "eth",
  name: "Ethereum",
  nativeSymbol: "ETH",
  pollCost: 2,
  normalize: (a) => a,
  bootstrap: async () => "1",
  poll: async () => ({ events: [], cursor: "" }),
  balance: async () => ({ amount: 0, symbol: "ETH" }),
  txURL: (h) => "https://x/tx/" + h,
  addressURL: (a) => "https://x/a/" + a,
};

describe("alerts", () => {
  it("knows when a target is crossed", () => {
    expect(triggered("above", 100, 100)).toBe(true);
    expect(triggered("above", 100, 99.99)).toBe(false);
    expect(triggered("below", 100, 99)).toBe(true);
    expect(triggered("below", 100, 101)).toBe(false);
    expect(triggered("below", 100, 0)).toBe(false);
    expect(triggered("sideways", 1, 1)).toBe(false);
  });

  it("fires, retries failed sends and drops gone chats", async () => {
    const now = Date.now();
    const alerts: Alert[] = [
      { id: 1, userId: 1, chatId: 10, symbol: "BTC", direction: "above", target: 100000, createdAt: now },
      { id: 2, userId: 2, chatId: 20, symbol: "BTC", direction: "below", target: 50000, createdAt: now },
      { id: 3, userId: 3, chatId: 30, symbol: "ETH", direction: "above", target: 1000, createdAt: now },
      { id: 4, userId: 4, chatId: 40, symbol: "ETH", direction: "above", target: 1000, createdAt: now },
    ];
    const claimed = new Set<number>();
    const released: number[] = [];
    const deactivated: number[] = [];
    const st = {
      activeAlerts: async () => alerts,
      claimAlert: async (id: number) => (claimed.has(id) ? false : (claimed.add(id), true)),
      releaseAlert: async (id: number) => void (released.push(id), claimed.delete(id)),
      deactivateChat: async (chatId: number) => void deactivated.push(chatId),
    };
    const n = new FakeNotifier({ 30: new Error("telegram timeout"), 40: new RecipientGoneError("blocked") });
    const fired = await runAlerts(st, fakePrices({ BTC: 100500, ETH: 3000 }), n, new Budget(10), quietLogger);
    expect(fired).toBe(1);
    expect(n.sent[0]!.html).toContain("BTC");
    expect(released).toEqual([3]);
    expect(deactivated).toEqual([40]);
    expect([...claimed].sort()).toEqual([1, 4]);
  });
});

describe("wallet notifications", () => {
  it("values, filters and formats events", () => {
    const quotes = new Map([["ETH", q(2000)], ["USDC", q(1)]]);
    const base: ChainEvent = { txHash: "0x1", key: "native", direction: "in", counterparty: "", amount: 0, symbol: "", token: "", verified: true, block: 1, time: 0 };
    const eth = value({ ...base, amount: 1.5, symbol: "WETH", counterparty: "0xabcdef0123456789abcdef0123456789abcdef01" }, quotes);
    expect(eth.known).toBe(true);
    expect(eth.usd).toBe(3000);
    const spam = value({ ...base, txHash: "0x2", amount: 1e6, symbol: "USDC", verified: false }, quotes);
    expect(spam.known).toBe(false);
    expect([passes(0, spam), passes(100, spam), passes(1000, eth), passes(5000, eth)]).toEqual([true, false, true, false]);

    const msg = walletMessage(fakeTracker, { id: 7, address: "0xd8da6bf26964af9d7eed9e03e53415d37aa96045", label: "<Vitalik>" }, [spam, eth, eth], 2);
    for (const want of ["&lt;Vitalik&gt;", "Received 1.5 WETH", "≈ $3,000.00", "and 1 earlier", "/minusd 7"]) expect(msg).toContain(want);
    expect(msg).not.toContain("unverified");
  });

  it("polls due wallets within the budget and delivers new events", async () => {
    const d1 = new FakeD1();
    const st = new Store(d1);
    const u1 = await st.upsertUser(1, 101, "", "A");
    const u2 = await st.upsertUser(2, 102, "", "B");
    const w1 = await st.createWallet("eth", "0xaaa", "1");
    const w2 = await st.createWallet("eth", "0xbbb", "1");
    await st.subscribe(u1.id, w1, "Mine");
    const big = await st.subscribe(u2.id, w1, "");
    await st.setMinUSD(u2.id, big.id, 1e9);
    await st.subscribe(u1.id, w2, "");
    d1.db.exec("UPDATE wallets SET last_polled_at = 0");

    const polled: string[] = [];
    let budget = new Budget(0);
    const tracker: Tracker = {
      ...fakeTracker,
      async poll(address) {
        budget.take();
        budget.take();
        polled.push(address);
        return {
          events: [{ txHash: "0x" + address, key: "native", direction: "in", counterparty: "", amount: 1, symbol: "ETH", token: "", verified: true, block: 2, time: 0 }],
          cursor: "2",
        };
      },
    };
    const chains = { get: (id: string) => (id === "eth" ? tracker : undefined) };

    // Budget for one wallet only (cost 2) after keeping 8 back.
    budget = new Budget(11);
    expect(await pollWallets(st, chains, budget, quietLogger, 8)).toBe(1);
    expect(polled).toEqual(["0xaaa"]);

    const n = new FakeNotifier();
    await dispatchEvents(st, chains, fakePrices({ ETH: 2000 }), n, new Budget(10), quietLogger, 5);
    // u2's $1B threshold filters the transfer out.
    expect(n.sent.map((m) => m.chatId)).toEqual([101]);
    expect(n.sent[0]!.html).toContain("Mine");

    // The second wallet is next in line.
    budget = new Budget(20);
    expect(await pollWallets(st, chains, budget, quietLogger, 8)).toBe(1);
    expect(polled).toEqual(["0xaaa", "0xbbb"]);
  });

  it("puts events back when the budget can't cover the messages", async () => {
    const d1 = new FakeD1();
    const st = new Store(d1);
    const u = await st.upsertUser(1, 101, "", "A");
    const w = await st.createWallet("eth", "0xaaa", "1");
    await st.subscribe(u.id, w, "");
    const u2 = await st.upsertUser(2, 102, "", "B");
    await st.subscribe(u2.id, w, "");
    const ev: ChainEvent = { txHash: "0x1", key: "native", direction: "in", counterparty: "", amount: 1, symbol: "ETH", token: "", verified: true, block: 2, time: 0 };
    await st.saveEvents(w, [ev], "2");
    const chains = { get: () => fakeTracker };

    const n = new FakeNotifier();
    // Two subscribers need two messages; one request is left.
    await dispatchEvents(st, chains, fakePrices({}), n, new Budget(1), quietLogger, 5);
    expect(n.sent).toHaveLength(0);

    await dispatchEvents(st, chains, fakePrices({}), n, new Budget(5), quietLogger, 5);
    expect(n.sent.map((m) => m.chatId)).toEqual([101, 102]);
  });
});
