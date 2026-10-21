export interface Quote {
  symbol: string;
  price: number;
  change24h: number;
  hasChange: boolean;
  source: string;
}

// time is unix milliseconds.
export interface Point {
  time: number;
  price: number;
}

export interface MarketCoin {
  rank: number;
  symbol: string;
  name: string;
  price: number;
  change24h: number;
  marketCap: number;
}

export interface Provider {
  readonly name: string;
  quotes(symbols: string[], withChange: boolean): Promise<Map<string, Quote>>;
  history(symbol: string, days: number): Promise<Point[]>;
}

export interface TopProvider {
  top(n: number): Promise<MarketCoin[]>;
}

const symbolRe = /^[A-Z0-9]{1,15}$/;

const aliases: Record<string, string> = {
  XBT: "BTC",
  WBTC: "BTC",
  WETH: "ETH",
  WSOL: "SOL",
  WPOL: "POL",
  WMATIC: "POL",
  MATIC: "POL",
  "USDC.E": "USDC",
  WBNB: "BNB",
};

const pegged = new Set(["USDT", "USDC", "DAI", "FDUSD", "TUSD", "USDE", "PYUSD", "USDS"]);

export function normalize(symbol: string): string {
  let s = symbol.trim().toUpperCase();
  if (s.startsWith("$")) s = s.slice(1);
  return aliases[s] ?? s;
}

export function validSymbol(symbol: string): boolean {
  return symbolRe.test(normalize(symbol));
}

export function isPegged(symbol: string): boolean {
  return pegged.has(normalize(symbol));
}

export function chartWindow(days: number): [interval: string, limit: number] {
  if (days <= 1) return ["15m", 96];
  if (days <= 7) return ["1h", 168];
  if (days <= 30) return ["4h", 180];
  if (days <= 90) return ["1d", 90];
  return ["1d", 365];
}
