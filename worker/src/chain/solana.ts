import { BudgetExhaustedError, InvalidAddressError } from "../errors";
import { Http, Limiter } from "../http";
import { fromBaseUnits, type Balance, type ChainEvent, type PollResult, type Tracker } from "./chain";
import { solanaMints } from "./tokens";

const solPageSize = 25;
const solMaxPages = 3;
const solMaxTxs = 15;
const base58Chars = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz";
const solAddrRe = /^[1-9A-HJ-NP-Za-km-z]{32,44}$/;
const limiter = new Limiter(2);
// Highest transaction format the RPC should return. Version 1 transactions
// are live on mainnet; asking for less makes getTransaction fail for them.
const maxTxVersion = 1;

class SolanaRPCError extends Error {
  constructor(method: string, code: number, msg: string) {
    super(`solana ${method}: ${code} ${msg}`);
    this.name = "SolanaRPCError";
  }
}

interface SolSignature {
  signature: string;
  err: unknown;
  blockTime: number | null;
}

interface SolTokenBalance {
  accountIndex: number;
  mint: string;
  owner?: string;
  uiTokenAmount: { amount: string; decimals: number };
}

// getTransaction with "json" encoding: account keys are plain strings, and
// keys loaded from address lookup tables come back in meta.loadedAddresses.
export interface SolTx {
  slot: number;
  blockTime: number | null;
  meta: {
    err: unknown;
    fee: number;
    preBalances: number[];
    postBalances: number[];
    preTokenBalances?: SolTokenBalance[];
    postTokenBalances?: SolTokenBalance[];
    loadedAddresses?: { writable: string[]; readonly: string[] };
  } | null;
  transaction: { message: { accountKeys: (string | { pubkey: string })[] } };
}

export class Solana implements Tracker {
  readonly id = "sol";
  readonly name = "Solana";
  readonly nativeSymbol = "SOL";
  readonly pollCost = 2;

  constructor(
    private readonly http: Http,
    private readonly rpc: string,
  ) {}

  txURL(hash: string): string {
    return `https://solscan.io/tx/${hash}`;
  }

  addressURL(address: string): string {
    return `https://solscan.io/account/${address}`;
  }

  normalize(address: string): string {
    const a = address.trim();
    if (!solAddrRe.test(a) || decodedBase58Len(a) !== 32) throw new InvalidAddressError();
    return a;
  }

  private async call<T>(method: string, params: unknown[]): Promise<T | null> {
    let resp: { result?: T; error?: { code: number; message: string } };
    try {
      resp = await this.http.postJSON(this.rpc, { jsonrpc: "2.0", id: 1, method, params }, { limiter });
    } catch (err) {
      if (err instanceof BudgetExhaustedError) throw err;
      throw new Error(`solana ${method}: ${(err as Error).message}`);
    }
    if (resp.error) throw new SolanaRPCError(method, resp.error.code, resp.error.message);
    return resp.result ?? null;
  }

  private async signatures(address: string, until: string, before: string): Promise<SolSignature[]> {
    const opts: Record<string, unknown> = { limit: solPageSize, commitment: "confirmed" };
    if (until) opts.until = until;
    if (before) opts.before = before;
    return (await this.call<SolSignature[]>("getSignaturesForAddress", [address, opts])) ?? [];
  }

  async bootstrap(address: string): Promise<string> {
    const sigs = (await this.call<SolSignature[]>("getSignaturesForAddress", [address, { limit: 1, commitment: "confirmed" }])) ?? [];
    return sigs[0]?.signature ?? "";
  }

  async poll(address: string, cursor: string): Promise<PollResult> {
    const sigs: SolSignature[] = [];
    let before = "";
    for (let page = 0; page < solMaxPages; page++) {
      const batch = await this.signatures(address, cursor, before);
      sigs.push(...batch);
      if (batch.length < solPageSize || cursor === "") break;
      before = batch[batch.length - 1]!.signature;
    }
    if (sigs.length === 0) return { events: [], cursor };

    const ok: SolSignature[] = [];
    for (const sig of sigs) {
      if (sig.err === null || sig.err === undefined) ok.push(sig);
      if (ok.length === solMaxTxs) break;
    }
    // Oldest first, so a run that stops early can resume from the last
    // transaction it finished.
    const events: ChainEvent[] = [];
    let done = 0;
    for (let i = ok.length - 1; i >= 0; i--) {
      let tx: SolTx | null;
      try {
        tx = await this.call<SolTx>("getTransaction", [
          ok[i]!.signature,
          { encoding: "json", maxSupportedTransactionVersion: maxTxVersion, commitment: "confirmed" },
        ]);
      } catch (err) {
        // Out of budget or rate limited: keep what is done and resume from
        // there next run.
        if (done > 0 && !(err instanceof SolanaRPCError)) {
          return { events, cursor: ok[i + 1]!.signature };
        }
        // The node answered but can't serve this one transaction (e.g. a
        // newer format): skip it rather than stall the wallet forever.
        if (!(err instanceof SolanaRPCError)) throw err;
        tx = null;
      }
      if (tx) events.push(...buildSolanaEvents(address, ok[i]!.signature, tx));
      done++;
    }
    return { events, cursor: sigs[0]!.signature };
  }

  async balance(address: string): Promise<Balance> {
    const res = await this.call<{ value: number }>("getBalance", [address, { commitment: "confirmed" }]);
    return { amount: (res?.value ?? 0) / 1e9, symbol: "SOL" };
  }
}

export function buildSolanaEvents(owner: string, signature: string, tx: SolTx): ChainEvent[] {
  const m = tx.meta;
  if (!m || (m.err !== null && m.err !== undefined)) return [];
  const at = tx.blockTime ?? 0;
  const keys = tx.transaction.message.accountKeys.map((k) => (typeof k === "string" ? k : k.pubkey));
  if (m.loadedAddresses) keys.push(...m.loadedAddresses.writable, ...m.loadedAddresses.readonly);

  const events: ChainEvent[] = [];
  const idx = keys.indexOf(owner);
  if (idx >= 0 && idx < m.preBalances.length && idx < m.postBalances.length) {
    let delta = m.postBalances[idx]! - m.preBalances[idx]!;
    if (idx === 0) delta += m.fee;
    if (delta !== 0) {
      events.push({
        txHash: signature,
        key: "native",
        direction: delta < 0 ? "out" : "in",
        counterparty: solCounterparty(keys, m.preBalances, m.postBalances, idx, delta),
        amount: Math.abs(delta) / 1e9,
        symbol: "SOL",
        token: "",
        verified: true,
        block: tx.slot,
        time: at,
      });
    }
  }

  const deltas = new Map<string, { pre: bigint; post: bigint; decimals: number }>();
  const add = (bals: SolTokenBalance[] | undefined, post: boolean) => {
    for (const b of bals ?? []) {
      if (b.owner !== owner || !/^\d+$/.test(b.uiTokenAmount.amount)) continue;
      let d = deltas.get(b.mint);
      if (!d) {
        d = { pre: 0n, post: 0n, decimals: b.uiTokenAmount.decimals };
        deltas.set(b.mint, d);
      }
      const n = BigInt(b.uiTokenAmount.amount);
      if (post) d.post += n;
      else d.pre += n;
    }
  };
  add(m.preTokenBalances, false);
  add(m.postTokenBalances, true);

  for (const mint of [...deltas.keys()].sort()) {
    const d = deltas.get(mint)!;
    let diff = d.post - d.pre;
    if (diff === 0n) continue;
    const dir = diff < 0n ? "out" : "in";
    if (diff < 0n) diff = -diff;
    const known = solanaMints[mint];
    events.push({
      txHash: signature,
      key: `spl:${mint}`,
      direction: dir,
      counterparty: "",
      amount: fromBaseUnits(diff.toString(), d.decimals),
      symbol: known ?? `${mint.slice(0, 4)}…${mint.slice(-4)}`,
      token: mint,
      verified: known !== undefined,
      block: tx.slot,
      time: at,
    });
  }
  return events;
}

function solCounterparty(keys: string[], pre: number[], post: number[], self: number, delta: number): string {
  let best = "";
  let bestAbs = 0;
  for (let i = 0; i < keys.length; i++) {
    if (i === self || i >= pre.length || i >= post.length) continue;
    const d = post[i]! - pre[i]!;
    if ((delta > 0 && d < 0) || (delta < 0 && d > 0)) {
      if (Math.abs(d) > bestAbs) {
        best = keys[i]!;
        bestAbs = Math.abs(d);
      }
    }
  }
  return best;
}

function decodedBase58Len(s: string): number {
  let n = 0n;
  for (const ch of s) {
    const i = base58Chars.indexOf(ch);
    if (i < 0) return -1;
    n = n * 58n + BigInt(i);
  }
  let leading = 0;
  for (const ch of s) {
    if (ch !== "1") break;
    leading++;
  }
  const bytes = n === 0n ? 0 : Math.ceil(n.toString(16).length / 2);
  return leading + bytes;
}
