import { BudgetExhaustedError, InvalidAddressError, isStatus } from "../errors";
import { Http, Limiter } from "../http";
import type { Balance, ChainEvent, PollResult, Tracker } from "./chain";

const btcBase58Re = /^[13][a-km-zA-HJ-NP-Z1-9]{25,34}$/;
const btcBech32Re = /^bc1[02-9ac-hj-np-z]{11,87}$/;
const btcMaxPages = 3;
const limiter = new Limiter(3);

export interface EsploraTx {
  txid: string;
  vin: { prevout: { scriptpubkey_address?: string; value: number } | null }[];
  vout: { scriptpubkey_address?: string; value: number }[];
  status: { confirmed: boolean; block_height?: number; block_time?: number };
}

export class Bitcoin implements Tracker {
  readonly id = "btc";
  readonly name = "Bitcoin";
  readonly nativeSymbol = "BTC";
  readonly pollCost = 1;
  private readonly explorer = "https://mempool.space";

  constructor(
    private readonly http: Http,
    private readonly base: string,
  ) {}

  txURL(hash: string): string {
    return `${this.explorer}/tx/${hash}`;
  }

  addressURL(address: string): string {
    return `${this.explorer}/address/${address}`;
  }

  normalize(address: string): string {
    const a = address.trim();
    const lower = a.toLowerCase();
    if (lower.startsWith("bc1")) {
      if (btcBech32Re.test(lower)) return lower;
      throw new InvalidAddressError();
    }
    if (btcBase58Re.test(a)) return a;
    throw new InvalidAddressError();
  }

  async bootstrap(address: string): Promise<string> {
    await this.balance(address);
    let tip: string;
    try {
      tip = await this.http.text(`${this.base}/blocks/tip/height`, { limiter });
    } catch (err) {
      throw new Error(`bitcoin tip: ${(err as Error).message}`);
    }
    if (!/^\d+$/.test(tip)) throw new Error(`bitcoin tip ${JSON.stringify(tip)}`);
    return tip;
  }

  async poll(address: string, cursor: string): Promise<PollResult> {
    const last = Number(cursor) || 0;
    const all: EsploraTx[] = [];
    let url = `${this.base}/address/${address}/txs`;
    for (let page = 0; page < btcMaxPages; page++) {
      let txs: EsploraTx[];
      try {
        txs = await this.http.json<EsploraTx[]>(url, { limiter });
      } catch (err) {
        if (page > 0 && err instanceof BudgetExhaustedError) break;
        throw err;
      }
      all.push(...txs);
      const confirmed = txs.filter((tx) => tx.status.confirmed);
      const oldest = confirmed[confirmed.length - 1];
      if (confirmed.length < 25 || !oldest || (oldest.status.block_height ?? 0) <= last) break;
      url = `${this.base}/address/${address}/txs/chain/${oldest.txid}`;
    }
    const [events, maxHeight] = buildBitcoinEvents(address, all, last);
    return { events, cursor: String(Math.max(last, maxHeight)) };
  }

  async balance(address: string): Promise<Balance> {
    type Stats = { funded_txo_sum: number; spent_txo_sum: number };
    let res: { chain_stats: Stats; mempool_stats: Stats };
    try {
      res = await this.http.json(`${this.base}/address/${address}`, { limiter });
    } catch (err) {
      if (isStatus(err, 400)) throw new InvalidAddressError();
      throw new Error(`bitcoin balance: ${(err as Error).message}`);
    }
    const sats =
      res.chain_stats.funded_txo_sum - res.chain_stats.spent_txo_sum + res.mempool_stats.funded_txo_sum - res.mempool_stats.spent_txo_sum;
    return { amount: sats / 1e8, symbol: "BTC" };
  }
}

export function buildBitcoinEvents(owner: string, txs: EsploraTx[], after: number): [ChainEvent[], number] {
  const events: ChainEvent[] = [];
  let maxHeight = 0;
  const seen = new Set<string>();
  for (let i = txs.length - 1; i >= 0; i--) {
    const tx = txs[i]!;
    if (!tx.status.confirmed || seen.has(tx.txid)) continue;
    seen.add(tx.txid);
    const height = tx.status.block_height ?? 0;
    if (height > maxHeight) maxHeight = height;
    if (height <= after) continue;

    let received = 0;
    let sent = 0;
    let firstSender = "";
    let firstReceiver = "";
    for (const vin of tx.vin) {
      if (!vin.prevout) continue;
      const addr = vin.prevout.scriptpubkey_address ?? "";
      if (addr === owner) sent += vin.prevout.value;
      else if (firstSender === "") firstSender = addr;
    }
    for (const out of tx.vout) {
      const addr = out.scriptpubkey_address ?? "";
      if (addr === owner) received += out.value;
      else if (firstReceiver === "" && addr !== "") firstReceiver = addr;
    }
    const net = received - sent;
    if (net === 0) continue;
    events.push({
      txHash: tx.txid,
      key: "native",
      direction: net > 0 ? "in" : "out",
      counterparty: net > 0 ? firstSender : firstReceiver,
      amount: Math.abs(net) / 1e8,
      symbol: "BTC",
      token: "",
      verified: true,
      block: height,
      time: tx.status.block_time ?? 0,
    });
  }
  return [events, maxHeight];
}
