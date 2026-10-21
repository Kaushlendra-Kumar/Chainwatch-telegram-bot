import { expect, it } from "vitest";
import { Budget, Http } from "../src/http";
import { OKX } from "../src/price/okx";

function fakeOKX(handler: (url: URL) => unknown) {
  const calls: string[] = [];
  const http = new Http(new Budget(20), async (url) => {
    calls.push(url);
    return new Response(JSON.stringify({ code: "0", msg: "", data: handler(new URL(url)) }));
  });
  return { http, calls };
}

const tickers = [
  { instId: "BTC-USDT", last: "110", open24h: "100" },
  { instId: "ETH-USDT", last: "2000", open24h: "0" },
  { instId: "ETH-BTC", last: "0.02", open24h: "0.02" },
];

it("quotes every symbol and its 24h change from one call", async () => {
  const { http, calls } = fakeOKX(() => tickers);
  const okx = new OKX(http, "https://okx", { all: null, at: 0 });
  const q = await okx.quotes(["BTC", "ETH", "USDT", "NOPE"], true);
  expect(q.get("BTC")).toMatchObject({ price: 110, hasChange: true, source: "okx" });
  expect(q.get("BTC")!.change24h).toBeCloseTo(10, 9);
  expect(q.get("ETH")).toMatchObject({ price: 2000, hasChange: false });
  expect(q.get("USDT")!.price).toBe(1);
  expect(q.has("NOPE")).toBe(false);
  await okx.quotes(["BTC"], true);
  expect(calls).toHaveLength(1);
});

it("pages candles back for long ranges and returns them oldest first", async () => {
  const day = 86_400_000;
  const { http, calls } = fakeOKX((url) => {
    if (url.pathname.endsWith("/tickers")) return tickers;
    const limit = Number(url.searchParams.get("limit"));
    const after = Number(url.searchParams.get("after") ?? 400 * day);
    return Array.from({ length: limit }, (_, i) => {
      const t = after - (i + 1) * day;
      return [String(t), "0", "0", "0", String(t / day), "0"];
    });
  });
  const pts = await new OKX(http, "https://okx", { all: null, at: 0 }).history("BTC", 365);
  expect(pts).toHaveLength(365);
  expect(pts[0]!.time).toBeLessThan(pts[364]!.time);
  expect(new Set(pts.map((p) => p.time)).size).toBe(365);
  const candleCalls = calls.filter((u) => u.includes("candles"));
  expect(candleCalls).toHaveLength(2);
  expect(candleCalls[0]).toContain("bar=1Dutc&limit=300");
  expect(candleCalls[1]).toContain("limit=65&after=");
});
