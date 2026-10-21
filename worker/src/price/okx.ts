import { NotFoundError } from "../errors";
import { Http, Limiter } from "../http";
import { chartWindow, type Point, type Provider, type Quote } from "./types";

// OKX replaces Binance here: Binance answers 403 to requests from
// Cloudflare's network. One public call returns every spot ticker with its
// 24h open, so prices and 24h changes for all symbols cost one request.

interface Ticker {
  last: number;
  open24h: number;
}

export interface OKXState {
  all: Map<string, Ticker> | null;
  at: number;
}

const shared: OKXState = { all: null, at: 0 };
const limiter = new Limiter(10);
const maxCandles = 300;

const bars: Record<string, string> = { "15m": "15m", "1h": "1H", "4h": "4Hutc", "1d": "1Dutc" };

export class OKX implements Provider {
  readonly name = "okx";

  constructor(
    private readonly http: Http,
    private readonly base: string,
    private readonly state: OKXState = shared,
    private readonly ttlMs = 15_000,
  ) {}

  private async get<T>(path: string): Promise<T> {
    const res = await this.http.json<{ code: string; msg: string; data: T }>(`${this.base}${path}`, { limiter, timeoutMs: 10_000 });
    if (res.code !== "0") throw new Error(`okx: ${res.code} ${res.msg}`);
    return res.data;
  }

  private async tickers(): Promise<Map<string, Ticker>> {
    if (this.state.all && Date.now() - this.state.at < this.ttlMs) {
      return this.state.all;
    }
    let rows: { instId: string; last: string; open24h: string }[];
    try {
      rows = await this.get("/api/v5/market/tickers?instType=SPOT");
    } catch (err) {
      throw new Error(`okx prices: ${(err as Error).message}`);
    }
    const all = new Map<string, Ticker>();
    for (const r of rows) {
      if (!r.instId.endsWith("-USDT")) continue;
      const last = Number(r.last);
      if (!(last > 0)) continue;
      all.set(r.instId.slice(0, -5), { last, open24h: Number(r.open24h) });
    }
    this.state.all = all;
    this.state.at = Date.now();
    return all;
  }

  async quotes(symbols: string[], _withChange: boolean): Promise<Map<string, Quote>> {
    const all = await this.tickers();
    const out = new Map<string, Quote>();
    for (const s of symbols) {
      if (s === "USDT") {
        out.set(s, { symbol: s, price: 1, change24h: 0, hasChange: true, source: this.name });
        continue;
      }
      const t = all.get(s);
      if (!t) continue;
      const hasChange = t.open24h > 0;
      const change24h = hasChange ? ((t.last - t.open24h) / t.open24h) * 100 : 0;
      out.set(s, { symbol: s, price: t.last, change24h, hasChange, source: this.name });
    }
    return out;
  }

  async history(symbol: string, days: number): Promise<Point[]> {
    const all = await this.tickers();
    if (!all.has(symbol)) throw new NotFoundError("symbol not found");
    const [interval, limit] = chartWindow(days);
    const bar = bars[interval]!;
    const points: Point[] = [];
    let after = "";
    try {
      // Candles come newest first, at most 300 per call; page back with `after`.
      while (points.length < limit) {
        const n = Math.min(maxCandles, limit - points.length);
        const rows = await this.get<string[][]>(
          `/api/v5/market/candles?instId=${symbol}-USDT&bar=${bar}&limit=${n}${after ? `&after=${after}` : ""}`,
        );
        for (const r of rows) {
          const time = Number(r[0]);
          const price = Number(r[4]);
          if (Number.isFinite(time) && Number.isFinite(price)) points.push({ time, price });
        }
        if (rows.length < n) break;
        after = rows[rows.length - 1]![0]!;
      }
    } catch (err) {
      throw new Error(`okx candles: ${(err as Error).message}`);
    }
    if (points.length < 2) throw new NotFoundError("symbol not found");
    return points.reverse();
  }
}
