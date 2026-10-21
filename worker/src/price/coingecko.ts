import { NotFoundError } from "../errors";
import type { Http } from "../http";
import type { MarketCoin, Point, Provider, Quote, TopProvider } from "./types";

const coingeckoIDs: Record<string, string> = {
  BTC: "bitcoin",
  ETH: "ethereum",
  USDT: "tether",
  BNB: "binancecoin",
  SOL: "solana",
  XRP: "ripple",
  USDC: "usd-coin",
  DOGE: "dogecoin",
  ADA: "cardano",
  TRX: "tron",
  TON: "the-open-network",
  AVAX: "avalanche-2",
  SHIB: "shiba-inu",
  LINK: "chainlink",
  DOT: "polkadot",
  BCH: "bitcoin-cash",
  LTC: "litecoin",
  NEAR: "near",
  POL: "polygon-ecosystem-token",
  UNI: "uniswap",
  ATOM: "cosmos",
  XLM: "stellar",
  ETC: "ethereum-classic",
  APT: "aptos",
  ARB: "arbitrum",
  OP: "optimism",
  PEPE: "pepe",
  SUI: "sui",
  DAI: "dai",
  AAVE: "aave",
  FIL: "filecoin",
  HBAR: "hedera-hashgraph",
  ICP: "internet-computer",
  JUP: "jupiter-exchange-solana",
  BONK: "bonk",
  WIF: "dogwifcoin",
  INJ: "injective-protocol",
  RENDER: "render-token",
};

export interface CoinGeckoState {
  ids: Map<string, string>;
  missing: Map<string, number>;
  top: MarketCoin[];
  topAt: number;
}

export function newCoinGeckoState(): CoinGeckoState {
  return { ids: new Map(Object.entries(coingeckoIDs)), missing: new Map(), top: [], topAt: 0 };
}

const shared = newCoinGeckoState();

export class CoinGecko implements Provider, TopProvider {
  readonly name = "coingecko";
  private readonly headers: Record<string, string>;

  constructor(
    private readonly http: Http,
    private readonly base: string,
    apiKey: string,
    private readonly state: CoinGeckoState = shared,
  ) {
    this.headers = apiKey ? { "x-cg-demo-api-key": apiKey } : {};
  }

  private get<T>(url: string): Promise<T> {
    return this.http.json<T>(url, { headers: this.headers, timeoutMs: 15_000 });
  }

  private async resolve(symbol: string): Promise<string> {
    const known = this.state.ids.get(symbol);
    if (known) return known;
    const until = this.state.missing.get(symbol);
    if (until && Date.now() < until) throw new NotFoundError("symbol not found");

    let res: { coins?: { id: string; symbol: string; market_cap_rank: number | null }[] };
    try {
      res = await this.get(`${this.base}/search?query=${encodeURIComponent(symbol)}`);
    } catch (err) {
      throw new Error(`coingecko search: ${(err as Error).message}`);
    }
    const unranked = 2 ** 30;
    let best = "";
    let bestRank = unranked;
    for (const c of res.coins ?? []) {
      if (c.symbol.toUpperCase() !== symbol.toUpperCase()) continue;
      const rank = c.market_cap_rank && c.market_cap_rank > 0 ? c.market_cap_rank : unranked;
      if (best === "" || rank < bestRank) {
        best = c.id;
        bestRank = rank;
      }
    }
    if (best === "") {
      this.state.missing.set(symbol, Date.now() + 3_600_000);
      throw new NotFoundError("symbol not found");
    }
    this.state.ids.set(symbol, best);
    return best;
  }

  async quotes(symbols: string[], _withChange: boolean): Promise<Map<string, Quote>> {
    const bySymbol = new Map<string, string>();
    let lastErr: unknown = null;
    for (const s of symbols) {
      try {
        bySymbol.set(s, await this.resolve(s));
      } catch (err) {
        if (!(err instanceof NotFoundError)) lastErr = err;
      }
    }
    const out = new Map<string, Quote>();
    if (bySymbol.size === 0) {
      if (lastErr) throw lastErr;
      return out;
    }
    const ids = [...new Set(bySymbol.values())].join(",");
    let res: Record<string, { usd?: number; usd_24h_change?: number }>;
    try {
      res = await this.get(`${this.base}/simple/price?ids=${encodeURIComponent(ids)}&vs_currencies=usd&include_24hr_change=true`);
    } catch (err) {
      throw new Error(`coingecko price: ${(err as Error).message}`);
    }
    for (const [s, id] of bySymbol) {
      const row = res[id];
      if (!row || !(row.usd && row.usd > 0)) continue;
      const hasChange = typeof row.usd_24h_change === "number";
      out.set(s, { symbol: s, price: row.usd, change24h: row.usd_24h_change ?? 0, hasChange, source: this.name });
    }
    return out;
  }

  async history(symbol: string, days: number): Promise<Point[]> {
    const id = await this.resolve(symbol);
    let res: { prices?: [number, number][] };
    try {
      res = await this.get(`${this.base}/coins/${encodeURIComponent(id)}/market_chart?vs_currency=usd&days=${days}`);
    } catch (err) {
      throw new Error(`coingecko chart: ${(err as Error).message}`);
    }
    const points = (res.prices ?? []).map(([t, p]) => ({ time: Math.trunc(t), price: p }));
    if (points.length < 2) throw new NotFoundError("symbol not found");
    return points;
  }

  async top(n: number): Promise<MarketCoin[]> {
    if (this.state.top.length >= n && Date.now() - this.state.topAt < 600_000) {
      return this.state.top.slice(0, n);
    }
    let rows: {
      symbol: string;
      name: string;
      current_price: number;
      market_cap: number;
      market_cap_rank: number;
      price_change_percentage_24h: number | null;
    }[];
    try {
      rows = await this.get(`${this.base}/coins/markets?vs_currency=usd&order=market_cap_desc&per_page=${n}&page=1`);
    } catch (err) {
      throw new Error(`coingecko markets: ${(err as Error).message}`);
    }
    const coins = rows.map((r) => ({
      rank: r.market_cap_rank,
      symbol: r.symbol.toUpperCase(),
      name: r.name,
      price: r.current_price,
      marketCap: r.market_cap,
      change24h: r.price_change_percentage_24h ?? 0,
    }));
    this.state.top = coins;
    this.state.topAt = Date.now();
    return coins.slice(0, n);
  }
}
