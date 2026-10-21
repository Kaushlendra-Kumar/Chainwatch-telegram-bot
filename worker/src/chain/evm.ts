import { InvalidAddressError } from "../errors";
import { Http, Limiter, sleep } from "../http";
import { direction, fromBaseUnits, type Balance, type ChainEvent, type Direction, type PollResult, type Tracker } from "./chain";
import { evmTokens } from "./tokens";

export interface EVMNetwork {
  id: string;
  name: string;
  chainId: number;
  native: string;
  explorer: string;
  lagBlocks: number;
}

export const EVM_NETWORKS: Record<string, EVMNetwork> = {
  eth: { id: "eth", name: "Ethereum", chainId: 1, native: "ETH", explorer: "https://etherscan.io", lagBlocks: 3 },
  polygon: { id: "polygon", name: "Polygon", chainId: 137, native: "POL", explorer: "https://polygonscan.com", lagBlocks: 20 },
  arbitrum: { id: "arbitrum", name: "Arbitrum", chainId: 42161, native: "ETH", explorer: "https://arbiscan.io", lagBlocks: 200 },
  linea: { id: "linea", name: "Linea", chainId: 59144, native: "ETH", explorer: "https://lineascan.build", lagBlocks: 20 },
  bsc: { id: "bsc", name: "BNB Chain", chainId: 56, native: "BNB", explorer: "https://bscscan.com", lagBlocks: 20 },
  base: { id: "base", name: "Base", chainId: 8453, native: "ETH", explorer: "https://basescan.org", lagBlocks: 30 },
};

const evmAddrRe = /^0x[0-9a-fA-F]{40}$/;
const etherscanLimiter = new Limiter(2.5);

interface Envelope {
  status?: string;
  message?: string;
  result?: unknown;
  error?: { message: string };
}

export class Etherscan {
  constructor(
    private readonly http: Http,
    private readonly key: string,
    private readonly base = "https://api.etherscan.io/v2/api",
    private readonly limiter = etherscanLimiter,
  ) {}

  async query(chainId: number, params: Record<string, string>): Promise<unknown> {
    for (let attempt = 0; ; attempt++) {
      const qs = new URLSearchParams({ ...params, chainid: String(chainId), apikey: this.key });
      let env: Envelope;
      try {
        env = await this.http.json<Envelope>(`${this.base}?${qs}`, { limiter: this.limiter, timeoutMs: 20_000 });
      } catch (err) {
        throw new Error(`etherscan: ${(err as Error).message}`);
      }
      if (env.error) throw new Error(`etherscan: ${env.error.message}`);
      if (env.status === "0") {
        const msg = (env.message ?? "").toLowerCase();
        if (msg.includes("no transactions found") || msg.includes("no records found")) return [];
        const detail = typeof env.result === "string" ? env.result : "";
        if (attempt === 0 && /rate limit/i.test(detail)) {
          await sleep(1100);
          continue;
        }
        throw new Error(`etherscan: ${env.message}: ${detail}`);
      }
      return env.result;
    }
  }
}

export interface EtherscanTx {
  blockNumber: string;
  timeStamp: string;
  hash: string;
  from: string;
  to: string;
  value: string;
  isError?: string;
  contractAddress?: string;
  tokenSymbol?: string;
  tokenDecimal?: string;
}

export class EVM implements Tracker {
  readonly pollCost = 2;

  constructor(
    private readonly net: EVMNetwork,
    private readonly api: Etherscan | null,
  ) {}

  get id() {
    return this.net.id;
  }
  get name() {
    return this.net.name;
  }
  get nativeSymbol() {
    return this.net.native;
  }

  txURL(hash: string): string {
    return `${this.net.explorer}/tx/${hash}`;
  }

  addressURL(address: string): string {
    return `${this.net.explorer}/address/${address}`;
  }

  normalize(address: string): string {
    const a = address.trim();
    if (!evmAddrRe.test(a)) throw new InvalidAddressError();
    return a.toLowerCase();
  }

  private get etherscan(): Etherscan {
    if (!this.api) throw new Error("ETHERSCAN_API_KEY not set");
    return this.api;
  }

  async bootstrap(_address: string): Promise<string> {
    const hex = await this.etherscan.query(this.net.chainId, { module: "proxy", action: "eth_blockNumber" });
    if (typeof hex !== "string" || !/^0x[0-9a-f]+$/i.test(hex)) {
      throw new Error(`etherscan block number ${JSON.stringify(hex)}`);
    }
    return String(parseInt(hex, 16));
  }

  async poll(address: string, cursor: string): Promise<PollResult> {
    const last = Number(cursor) || 0;
    const start = Math.max(last - this.net.lagBlocks, 0);
    const native = await this.list("txlist", address, start);
    const tokens = await this.list("tokentx", address, start);
    const [events, maxBlock] = buildEVMEvents(address, this.net, native, tokens);
    return { events, cursor: String(Math.max(last, maxBlock)) };
  }

  private async list(action: string, address: string, start: number): Promise<EtherscanTx[]> {
    const rows = await this.etherscan.query(this.net.chainId, {
      module: "account",
      action,
      address,
      startblock: String(start),
      endblock: "99999999",
      page: "1",
      offset: "100",
      sort: "desc",
    });
    if (!Array.isArray(rows)) throw new Error(`etherscan ${action}: unexpected result`);
    return rows as EtherscanTx[];
  }

  async balance(address: string): Promise<Balance> {
    const wei = await this.etherscan.query(this.net.chainId, { module: "account", action: "balance", address, tag: "latest" });
    if (typeof wei !== "string") throw new Error("etherscan balance: unexpected result");
    return { amount: fromBaseUnits(wei, 18), symbol: this.net.native };
  }
}

export function buildEVMEvents(owner: string, net: EVMNetwork, native: EtherscanTx[], tokens: EtherscanTx[]): [ChainEvent[], number] {
  owner = owner.toLowerCase();
  const events: ChainEvent[] = [];
  let maxBlock = 0;
  const track = (tx: EtherscanTx): [number, number] => {
    const block = parseInt(tx.blockNumber, 10) || 0;
    if (block > maxBlock) maxBlock = block;
    return [block, parseInt(tx.timeStamp, 10) || 0];
  };
  const amountOf = (value: string, decimals: number): number => {
    try {
      return fromBaseUnits(value, decimals);
    } catch {
      return 0;
    }
  };

  for (let i = native.length - 1; i >= 0; i--) {
    const tx = native[i]!;
    const [block, at] = track(tx);
    if (tx.isError === "1" || !tx.value || tx.value === "0") continue;
    const amount = amountOf(tx.value, 18);
    if (amount === 0) continue;
    const from = tx.from.toLowerCase();
    const to = (tx.to ?? "").toLowerCase();
    const dir = direction(owner, from, to);
    events.push({
      txHash: tx.hash,
      key: "native",
      direction: dir,
      counterparty: counterparty(dir, from, to),
      amount,
      symbol: net.native,
      token: "",
      verified: true,
      block,
      time: at,
    });
  }

  const known = evmTokens[net.chainId] ?? {};
  for (let i = tokens.length - 1; i >= 0; i--) {
    const tx = tokens[i]!;
    const [block, at] = track(tx);
    if (!tx.value || tx.value === "0") continue;
    const decimals = /^\d+$/.test(tx.tokenDecimal ?? "") ? Number(tx.tokenDecimal) : 18;
    const amount = amountOf(tx.value, decimals);
    if (amount === 0) continue;
    const contract = (tx.contractAddress ?? "").toLowerCase();
    const knownSymbol = known[contract];
    const from = tx.from.toLowerCase();
    const to = (tx.to ?? "").toLowerCase();
    const dir = direction(owner, from, to);
    events.push({
      txHash: tx.hash,
      key: `erc20:${contract}:${from}:${to}:${tx.value}`,
      direction: dir,
      counterparty: counterparty(dir, from, to),
      amount,
      symbol: knownSymbol ?? sanitizeSymbol(tx.tokenSymbol ?? ""),
      token: contract,
      verified: knownSymbol !== undefined,
      block,
      time: at,
    });
  }
  return [events, maxBlock];
}

function counterparty(dir: Direction, from: string, to: string): string {
  if (dir === "in") return from;
  if (dir === "out") return to;
  return "";
}

export function sanitizeSymbol(s: string): string {
  let out = "";
  for (const ch of s) {
    if (/[A-Za-z0-9._-]/.test(ch)) out += ch;
    if (out.length >= 12) break;
  }
  return out === "" ? "TOKEN" : out.toUpperCase();
}
