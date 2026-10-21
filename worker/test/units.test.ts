import { describe, expect, it } from "vitest";
import { RateLimiter, summarize } from "../src/bot";
import { chartConfig } from "../src/chart";
import * as format from "../src/format";
import { parseDays, parseDirection, parseID, parseNumber } from "../src/parse";
import { PriceService, newPriceCache } from "../src/price/service";
import type { Point, Provider, Quote } from "../src/price/types";

describe("format", () => {
  it("formats USD", () => {
    const cases: [number, string][] = [
      [0, "$0.00"],
      [1, "$1.00"],
      [63421.123, "$63,421.12"],
      [1234567.5, "$1,234,567.50"],
      [0.5, "$0.5000"],
      [0.00001234, "$0.00001234"],
      [-1500, "-$1,500.00"],
      [999.999, "$1,000.00"],
      [0.000000123456, "$0.0000001235"],
    ];
    for (const [v, want] of cases) expect(format.usd(v)).toBe(want);
  });

  it("formats amounts, percents and compact values", () => {
    const cases: [number, string][] = [
      [0, "0"],
      [1.5, "1.5"],
      [2000, "2,000"],
      [12345.678, "12,345.68"],
      [0.00012345, "0.00012345"],
      [3.14159265, "3.1416"],
    ];
    for (const [v, want] of cases) expect(format.amount(v)).toBe(want);
    expect(["+2.35%", "+2.34%"]).toContain(format.percent(2.345));
    expect(format.percent(-1)).toBe("-1.00%");
    expect(format.percent(NaN)).toBe("n/a");
    expect(format.compactUSD(1.23e12)).toBe("$1.23T");
    expect(format.compactUSD(4.5e6)).toBe("$4.50M");
  });

  it("shortens addresses, escapes and formats ages", () => {
    expect(format.shortAddr("0xd8da6bf26964af9d7eed9e03e53415d37aa96045")).toBe("0xd8da…6045");
    expect(format.shortAddr("short")).toBe("short");
    expect(format.escape(`<a href="x">&'`)).toBe("&lt;a href=&#34;x&#34;&gt;&amp;&#39;");
    const now = Date.now();
    expect(format.ago(now - 30_000, now)).toBe("just now");
    expect(format.ago(now - 5 * 60_000, now)).toBe("5m ago");
    expect(format.ago(now - 3 * 3_600_000, now)).toBe("3h ago");
    expect(format.ago(now - 3 * 86_400_000, now)).toBe("3d ago");
  });
});

describe("parse", () => {
  it("parses numbers with shortcuts", () => {
    const ok: [string, number][] = [
      ["100000", 100000],
      ["100k", 100000],
      ["1.5M", 1500000],
      ["$2,500", 2500],
      ["0.0001", 0.0001],
      ["1_000", 1000],
      [" 2b ", 2e9],
      ["$64,250.5", 64250.5],
    ];
    for (const [s, want] of ok) expect(parseNumber(s)).toBeCloseTo(want, 9);
    for (const s of ["", "abc", "-5", "0", "NaN", "1e999", "k"]) expect(parseNumber(s)).toBeNull();
  });

  it("parses directions, ids and ranges", () => {
    expect(parseDirection(">")).toBe("above");
    expect(parseDirection("Below")).toBe("below");
    expect(parseDirection("sideways")).toBeNull();
    expect(parseID("#12")).toBe(12);
    expect(parseID("-3")).toBeNull();
    expect(parseDays("")).toBe(7);
    expect(parseDays("1y")).toBe(365);
    expect(parseDays("2w")).toBeNull();
  });
});

describe("bot helpers", () => {
  it("summarizes a portfolio", () => {
    const holdings = [
      { symbol: "BTC", quantity: 0.5, avgCost: 60000 },
      { symbol: "ETH", quantity: 2, avgCost: 3000 },
      { symbol: "XYZ", quantity: 10, avgCost: 1 },
    ];
    const q = (price: number): Quote => ({ symbol: "", price, change24h: 0, hasChange: false, source: "t" });
    const s = summarize(holdings, new Map([["BTC", q(70000)], ["ETH", q(2500)]]));
    expect([s.value, s.cost, s.pnl]).toEqual([40000, 36000, 4000]);
    expect(s.lines[0]!.pnl).toBe(5000);
    expect(s.lines[0]!.pnlPct).toBeCloseTo(16.6666667, 6);
    expect(s.lines[1]!.pnl).toBe(-1000);
    expect(s.lines[1]!.weight).toBe(12.5);
    expect(s.lines[2]!.known).toBe(false);
  });

  it("rate limits per user", () => {
    const r = new RateLimiter(1, 2);
    const now = Date.now();
    expect(r.allow(1, now) && r.allow(1, now)).toBe(true);
    expect(r.allow(1, now)).toBe(false);
    expect(r.allow(2, now)).toBe(true);
    expect(r.allow(1, now + 1100)).toBe(true);
  });

  it("builds a chart config with a tick formatter", () => {
    const points: Point[] = Array.from({ length: 500 }, (_, i) => ({ time: 1700000000000 + i * 3600_000, price: 100 + i }));
    const cfg = chartConfig("BTC / USD · 30d", points, 30);
    expect(cfg).toContain("(v) => '$'");
    expect(cfg).toContain("#16a34a");
    expect(cfg).not.toContain("__TICK__");
  });
});

class FakeProvider implements Provider {
  calls = 0;
  constructor(
    readonly name: string,
    private readonly prices: Record<string, number>,
    private readonly err: Error | null = null,
  ) {}
  async quotes(symbols: string[]): Promise<Map<string, Quote>> {
    this.calls++;
    if (this.err) throw this.err;
    const out = new Map<string, Quote>();
    for (const s of symbols) {
      const p = this.prices[s];
      if (p !== undefined) out.set(s, { symbol: s, price: p, change24h: 0, hasChange: true, source: this.name });
    }
    return out;
  }
  async history(): Promise<Point[]> {
    throw new Error("unused");
  }
}

describe("price service", () => {
  it("uses the fallback for missing symbols", async () => {
    const s = new PriceService(new FakeProvider("p", { BTC: 100 }), new FakeProvider("f", { XYZ: 2 }), null, undefined, newPriceCache());
    const got = await s.quotes(["btc", "xyz"], false);
    expect(got.get("BTC")!.price).toBe(100);
    expect(got.get("XYZ")!.source).toBe("f");
  });

  it("caches and normalizes aliases", async () => {
    const p = new FakeProvider("p", { ETH: 3000 });
    const s = new PriceService(p, null, null, undefined, newPriceCache());
    for (let i = 0; i < 3; i++) expect((await s.quotes(["WETH"], false)).get("ETH")!.price).toBe(3000);
    expect(p.calls).toBe(1);
  });

  it("negatively caches unknown symbols and pegs stablecoins", async () => {
    const p = new FakeProvider("p", {});
    const s = new PriceService(p, null, null, undefined, newPriceCache());
    const got = await s.quotes(["NOPE", "USDC"], false);
    expect(got.has("NOPE")).toBe(false);
    expect(got.get("USDC")!.price).toBe(1);
    await s.quotes(["NOPE"], false);
    expect(p.calls).toBe(1);
  });

  it("errors when every provider fails", async () => {
    const boom = new Error("down");
    const s = new PriceService(new FakeProvider("p", {}, boom), new FakeProvider("f", {}, boom), null, undefined, newPriceCache());
    await expect(s.quote("BTC", false)).rejects.toThrow("down");
  });
});
