import { NotFoundError } from "../errors";
import { Http, Limiter } from "../http";
import { chartWindow, type Point, type Provider, type Quote } from "./types";

export interface BinanceState {
  all: Map<string, number> | null;
  at: number;
}

const shared: BinanceState = { all: null, at: 0 };
const limiter = new Limiter(10);

export class Binance implements Provider {
  readonly name = "binance";

  constructor(
    private readonly http: Http,
    private readonly base: string,
    private readonly state: BinanceState = shared,
    private readonly ttlMs = 15_000,
  ) {}

  // One call returns every price; it is cached and shared by every caller in
  // the isolate.
  private async allPrices(): Promise<Map<string, number>> {
    if (this.state.all && Date.now() - this.state.at < this.ttlMs) {
      return this.state.all;
    }
    let rows: { symbol: string; price: string }[];
    try {
      rows = await this.http.json(`${this.base}/api/v3/ticker/price`, { limiter, timeoutMs: 10_000 });
    } catch (err) {
      throw new Error(`binance prices: ${(err as Error).message}`);
    }
    const all = new Map<string, number>();
    for (const r of rows) {
      if (!r.symbol.endsWith("USDT") || r.symbol === "USDT") continue;
      const p = Number(r.price);
      if (!(p > 0)) continue;
      all.set(r.symbol.slice(0, -4), p);
    }
    this.state.all = all;
    this.state.at = Date.now();
    return all;
  }

  async quotes(symbols: string[], withChange: boolean): Promise<Map<string, Quote>> {
    const all = await this.allPrices();
    const out = new Map<string, Quote>();
    const found: string[] = [];
    for (const s of symbols) {
      if (s === "USDT") {
        out.set(s, { symbol: s, price: 1, change24h: 0, hasChange: true, source: this.name });
        continue;
      }
      const p = all.get(s);
      if (p !== undefined) {
        out.set(s, { symbol: s, price: p, change24h: 0, hasChange: false, source: this.name });
        found.push(s);
      }
    }
    if (!withChange || found.length === 0) {
      return out;
    }
    for (let start = 0; start < found.length; start += 100) {
      const pairs = found.slice(start, start + 100).map((s) => s + "USDT");
      let rows: { symbol: string; lastPrice: string; priceChangePercent: string }[];
      try {
        rows = await this.http.json(
          `${this.base}/api/v3/ticker/24hr?symbols=${encodeURIComponent(JSON.stringify(pairs))}`,
          { limiter, timeoutMs: 10_000 },
        );
      } catch {
        return out;
      }
      for (const r of rows) {
        const s = r.symbol.replace(/USDT$/, "");
        const q = out.get(s);
        if (!q) continue;
        const p = Number(r.lastPrice);
        if (p > 0) q.price = p;
        const c = Number(r.priceChangePercent);
        if (r.priceChangePercent !== "" && Number.isFinite(c)) {
          q.change24h = c;
          q.hasChange = true;
        }
      }
    }
    return out;
  }

  async history(symbol: string, days: number): Promise<Point[]> {
    const all = await this.allPrices();
    if (!all.has(symbol)) {
      throw new NotFoundError("symbol not found");
    }
    const [interval, limit] = chartWindow(days);
    let rows: unknown[][];
    try {
      rows = await this.http.json(
        `${this.base}/api/v3/klines?symbol=${symbol}USDT&interval=${interval}&limit=${limit}`,
        { limiter, timeoutMs: 10_000 },
      );
    } catch (err) {
      throw new Error(`binance klines: ${(err as Error).message}`);
    }
    const points: Point[] = [];
    for (const r of rows) {
      if (r.length < 5 || typeof r[0] !== "number") continue;
      const p = Number(r[4]);
      if (!Number.isFinite(p)) continue;
      points.push({ time: r[0], price: p });
    }
    if (points.length < 2) {
      throw new NotFoundError("symbol not found");
    }
    return points;
  }
}
