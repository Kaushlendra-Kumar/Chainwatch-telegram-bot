import { message, NotFoundError } from "../errors";
import { quietLogger, type Logger } from "../log";
import { isPegged, normalize, type MarketCoin, type Point, type Provider, type Quote, type TopProvider } from "./types";

export interface PriceCache {
  quotes: Map<string, { quote: Quote; at: number }>;
  missing: Map<string, number>;
}

export function newPriceCache(): PriceCache {
  return { quotes: new Map(), missing: new Map() };
}

const shared = newPriceCache();

export class PriceService {
  ttlMs = 20_000;
  fallbackTTLMs = 300_000;
  missingTTLMs = 1_800_000;
  now = () => Date.now();

  constructor(
    private readonly primary: Provider | null,
    private readonly fallback: Provider | null,
    private readonly topProvider: TopProvider | null,
    private readonly log: Logger = quietLogger,
    private readonly cache: PriceCache = shared,
  ) {}

  async quote(symbol: string, withChange: boolean): Promise<Quote> {
    const sym = normalize(symbol);
    const q = (await this.quotes([sym], withChange)).get(sym);
    if (!q) throw new NotFoundError("symbol not found");
    return q;
  }

  async quotes(symbols: string[], withChange: boolean): Promise<Map<string, Quote>> {
    const out = new Map<string, Quote>();
    const need: string[] = [];
    const seen = new Set<string>();
    const now = this.now();
    for (const raw of symbols) {
      const sym = normalize(raw);
      if (sym === "" || seen.has(sym)) continue;
      seen.add(sym);
      const e = this.cache.quotes.get(sym);
      if (e && now - e.at < this.ttlFor(e.quote) && (!withChange || e.quote.hasChange)) {
        out.set(sym, e.quote);
        continue;
      }
      const until = this.cache.missing.get(sym);
      if (until && now < until) continue;
      need.push(sym);
    }
    if (need.length === 0) return out;

    const errs: string[] = [];
    let rest = need;
    for (const p of [this.primary, this.fallback]) {
      if (!p || rest.length === 0) continue;
      let got = new Map<string, Quote>();
      try {
        got = await p.quotes(rest, withChange);
      } catch (err) {
        errs.push(message(err));
        this.log.debug("price provider failed", { provider: p.name, err });
      }
      this.store(got);
      const next: string[] = [];
      for (const sym of rest) {
        const q = got.get(sym);
        if (q) out.set(sym, q);
        else next.push(sym);
      }
      rest = next;
    }
    for (const sym of rest) {
      if (isPegged(sym)) {
        const q: Quote = { symbol: sym, price: 1, change24h: 0, hasChange: true, source: "peg" };
        out.set(sym, q);
        this.cache.quotes.set(sym, { quote: q, at: this.now() });
        continue;
      }
      if (errs.length === 0) this.cache.missing.set(sym, this.now() + this.missingTTLMs);
    }
    if (out.size === 0 && errs.length > 0) {
      throw new Error(errs.join("; "));
    }
    return out;
  }

  async history(symbol: string, days: number): Promise<Point[]> {
    const sym = normalize(symbol);
    let lastErr: unknown = new NotFoundError("symbol not found");
    for (const p of [this.primary, this.fallback]) {
      if (!p) continue;
      try {
        return await p.history(sym, days);
      } catch (err) {
        lastErr = err;
      }
    }
    throw lastErr;
  }

  top(n: number): Promise<MarketCoin[]> {
    if (!this.topProvider) return Promise.reject(new Error("top list provider not configured"));
    return this.topProvider.top(n);
  }

  private ttlFor(q: Quote): number {
    return this.primary && q.source === this.primary.name ? this.ttlMs : this.fallbackTTLMs;
  }

  private store(quotes: Map<string, Quote>): void {
    const now = this.now();
    for (const [sym, q] of quotes) {
      this.cache.quotes.set(sym, { quote: q, at: now });
      this.cache.missing.delete(sym);
    }
  }
}
