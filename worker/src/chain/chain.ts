export type Direction = "in" | "out" | "self";

// time is unix seconds; 0 means unknown.
export interface ChainEvent {
  txHash: string;
  key: string;
  direction: Direction;
  counterparty: string;
  amount: number;
  symbol: string;
  token: string;
  verified: boolean;
  block: number;
  time: number;
}

export interface Balance {
  amount: number;
  symbol: string;
}

export interface PollResult {
  events: ChainEvent[];
  cursor: string;
}

export interface Tracker {
  readonly id: string;
  readonly name: string;
  readonly nativeSymbol: string;
  // Outbound requests one ordinary poll needs.
  readonly pollCost: number;
  normalize(address: string): string;
  bootstrap(address: string): Promise<string>;
  poll(address: string, cursor: string): Promise<PollResult>;
  balance(address: string): Promise<Balance>;
  txURL(hash: string): string;
  addressURL(address: string): string;
}

const chainAliases: Record<string, string> = {
  ethereum: "eth",
  ether: "eth",
  matic: "polygon",
  pol: "polygon",
  arb: "arbitrum",
  bitcoin: "btc",
  solana: "sol",
};

export class Registry {
  private readonly trackers = new Map<string, Tracker>();

  constructor(...trackers: Tracker[]) {
    for (const t of trackers) this.trackers.set(t.id, t);
  }

  get(id: string): Tracker | undefined {
    let key = id.trim().toLowerCase();
    key = chainAliases[key] ?? key;
    return this.trackers.get(key);
  }

  all(): Tracker[] {
    return [...this.trackers.values()].sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
  }
}

export function fromBaseUnits(raw: string, decimals: number): number {
  const s = raw.trim();
  if (!/^[+-]?\d+$/.test(s)) {
    throw new Error("invalid integer amount");
  }
  const sign = s.startsWith("-") ? -1 : 1;
  const digits = s.replace(/^[+-]/, "").replace(/^0+/, "") || "0";
  if (decimals <= 0) return sign * Number(digits);
  const padded = digits.padStart(decimals + 1, "0");
  return sign * Number(`${padded.slice(0, -decimals)}.${padded.slice(-decimals)}`);
}

export function direction(owner: string, from: string, to: string): Direction {
  if (from === owner && to === owner) return "self";
  if (to === owner) return "in";
  return "out";
}
