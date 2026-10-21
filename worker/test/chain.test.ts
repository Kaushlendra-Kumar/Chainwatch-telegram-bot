import { describe, expect, it } from "vitest";
import { Bitcoin } from "../src/chain/bitcoin";
import { Registry, fromBaseUnits } from "../src/chain/chain";
import { EVM, EVM_NETWORKS, buildEVMEvents, type EtherscanTx } from "../src/chain/evm";
import { Solana, buildSolanaEvents, type SolTx } from "../src/chain/solana";
import { BudgetExhaustedError } from "../src/errors";
import { Budget, Http } from "../src/http";

const vitalik = "0xd8da6bf26964af9d7eed9e03e53415d37aa96045";
const other = "0x1111111111111111111111111111111111111111";
const unusedHttp = new Http(new Budget(0));

describe("chain helpers", () => {
  it("converts base units", () => {
    expect(fromBaseUnits("1500000000000000000", 18)).toBeCloseTo(1.5, 12);
    expect(fromBaseUnits("2500000", 6)).toBe(2.5);
    expect(fromBaseUnits("5", 6)).toBe(0.000005);
    expect(() => fromBaseUnits("abc", 6)).toThrow();
  });

  it("normalizes addresses", () => {
    const evm = new EVM(EVM_NETWORKS.eth!, null);
    expect(evm.normalize("0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045")).toBe(vitalik);
    expect(() => evm.normalize("0x123")).toThrow();
    const btc = new Bitcoin(unusedHttp, "x");
    expect(btc.normalize("1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa")).toBe("1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa");
    expect(btc.normalize("BC1QAR0SRRR7XFKVY5L643LYDNW9RE59GTZZWF5MDQ")).toBe("bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq");
    expect(() => btc.normalize("0xabc")).toThrow();
    const sol = new Solana(unusedHttp, "x");
    expect(sol.normalize("EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v")).toBe("EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v");
    expect(() => sol.normalize("0OIl0OIl0OIl0OIl0OIl0OIl0OIl0OIl")).toThrow();
  });

  it("resolves chain aliases", () => {
    const r = new Registry(new EVM(EVM_NETWORKS.eth!, null), new Bitcoin(unusedHttp, "x"), new Solana(unusedHttp, "x"));
    expect(r.get("ethereum")!.id).toBe("eth");
    expect(r.get("BTC")!.id).toBe("btc");
    expect(r.get("solana")!.id).toBe("sol");
    expect(r.get("doge")).toBeUndefined();
    expect(r.all().map((t) => t.id)).toEqual(["btc", "eth", "sol"]);
  });
});

describe("EVM events", () => {
  it("builds native and token events, oldest first", () => {
    const native: EtherscanTx[] = [
      { blockNumber: "102", timeStamp: "1700000100", hash: "0xb", from: vitalik, to: other, value: "2000000000000000000" },
      { blockNumber: "101", timeStamp: "1700000050", hash: "0xfail", from: other, to: vitalik, value: "1", isError: "1" },
      { blockNumber: "100", timeStamp: "1700000000", hash: "0xa", from: other, to: "0X" + vitalik.slice(2), value: "1000000000000000000" },
      { blockNumber: "99", timeStamp: "1700000000", hash: "0xzero", from: vitalik, to: other, value: "0" },
    ];
    const tokens: EtherscanTx[] = [
      { blockNumber: "105", timeStamp: "1700000200", hash: "0xc", from: other, to: vitalik, value: "2500000", contractAddress: "0xA0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", tokenSymbol: "USDC", tokenDecimal: "6" },
      { blockNumber: "104", timeStamp: "1700000150", hash: "0xd", from: other, to: vitalik, value: "1000", contractAddress: "0xdeadbeef00000000000000000000000000000000", tokenSymbol: "U$DT<script>", tokenDecimal: "0" },
    ];
    const [events, maxBlock] = buildEVMEvents(vitalik, EVM_NETWORKS.eth!, native, tokens);
    expect(maxBlock).toBe(105);
    expect(events).toHaveLength(4);
    expect(events[0]).toMatchObject({ txHash: "0xa", direction: "in", amount: 1, counterparty: other });
    expect(events[1]).toMatchObject({ txHash: "0xb", direction: "out", amount: 2 });
    expect(events[3]).toMatchObject({ symbol: "USDC", verified: true, amount: 2.5 });
    expect(events[2]).toMatchObject({ verified: false, symbol: "UDTSCRIPT" });
  });
});

function fakeHttp(handler: (url: string, init: RequestInit) => unknown, budget = new Budget(100)) {
  return new Http(budget, async (url, init) => {
    const body = handler(url, init);
    if (body === undefined) return new Response("not found", { status: 404 });
    return new Response(JSON.stringify(body), { status: 200 });
  });
}

describe("Bitcoin poll", () => {
  it("returns new confirmed transfers and advances the cursor", async () => {
    const owner = "bc1qowner000000000000000000000000000000";
    const mk = (txid: string, height: number, inAddr: string, inVal: number, outs: Record<string, number>) => ({
      txid,
      vin: [{ prevout: { scriptpubkey_address: inAddr, value: inVal } }],
      vout: Object.entries(outs).map(([a, v]) => ({ scriptpubkey_address: a, value: v })),
      status: { confirmed: height > 0, block_height: height, block_time: 1700000000 + height },
    });
    const txs = [
      mk("pending", 0, "bc1qsender", 5000, { [owner]: 5000 }),
      mk("new-out", 801, owner, 100000, { bc1qdest: 60000, [owner]: 39000 }),
      mk("new-in", 800, "bc1qsender", 250000000, { [owner]: 150000000, bc1qchange: 99990000 }),
      mk("old", 799, "bc1qsender", 1000, { [owner]: 1000 }),
    ];
    const http = fakeHttp((url) => (url === `http://btc/address/${owner}/txs` ? txs : undefined));
    const res = await new Bitcoin(http, "http://btc").poll(owner, "799");
    expect(res.cursor).toBe("801");
    expect(res.events).toHaveLength(2);
    expect(res.events[0]).toMatchObject({ txHash: "new-in", direction: "in", amount: 1.5, counterparty: "bc1qsender" });
    expect(res.events[1]).toMatchObject({ txHash: "new-out", direction: "out", counterparty: "bc1qdest" });
    expect(res.events[1]!.amount).toBeCloseTo(0.00061, 12);
  });
});

describe("Solana", () => {
  const owner = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v";
  const dest = "DestPubkey111111111111111111111111111111111";
  const tx: SolTx = {
    slot: 300,
    blockTime: 1700000000,
    meta: {
      err: null,
      fee: 5000,
      preBalances: [2000000000, 100, 0],
      postBalances: [999995000, 1000000100, 0],
      preTokenBalances: [{ accountIndex: 2, mint: "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB", owner, uiTokenAmount: { amount: "1000000", decimals: 6 } }],
      postTokenBalances: [{ accountIndex: 2, mint: "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB", owner, uiTokenAmount: { amount: "4000000", decimals: 6 } }],
    },
    transaction: { message: { accountKeys: [owner, dest, "TokenAcct"] } },
  };

  it("builds SOL and SPL events", () => {
    const events = buildSolanaEvents(owner, "sig1", tx);
    expect(events).toHaveLength(2);
    expect(events[0]).toMatchObject({ direction: "out", amount: 1, counterparty: dest });
    expect(events[1]).toMatchObject({ symbol: "USDT", direction: "in", amount: 3, verified: true });
  });

  it("finds the owner among lookup-table addresses", () => {
    const moved: SolTx = {
      ...tx,
      meta: { ...tx.meta!, loadedAddresses: { writable: [owner], readonly: [] } },
      transaction: { message: { accountKeys: ["Payer", dest, "TokenAcct"] } },
    };
    moved.meta!.preBalances = [10, 0, 0, 500];
    moved.meta!.postBalances = [5, 0, 0, 2500];
    const events = buildSolanaEvents(owner, "sig2", moved);
    expect(events[0]).toMatchObject({ key: "native", direction: "in", amount: 2000 / 1e9 });
  });

  it("stops at the budget and resumes from the last finished transaction", async () => {
    const sigs = ["s3", "s2", "s1"].map((signature) => ({ signature, err: null, blockTime: 1 }));
    const budget = new Budget(3); // signatures + two transactions
    const http = fakeHttp((_url, init) => {
      const body = JSON.parse(String(init.body));
      if (body.method === "getSignaturesForAddress") return { result: sigs };
      return { result: tx };
    }, budget);
    const res = await new Solana(http, "http://sol").poll(owner, "s0");
    expect(res.events).toHaveLength(4);
    expect(res.cursor).toBe("s2");

    await expect(new Solana(fakeHttp(() => ({ result: sigs }), new Budget(1)), "http://sol").poll(owner, "s0")).rejects.toBeInstanceOf(
      BudgetExhaustedError,
    );
  });

  it("skips a transaction the node can't serve and keeps progress on rate limits", async () => {
    const sigs = ["s3", "s2", "s1"].map((signature) => ({ signature, err: null, blockTime: 1 }));
    const unsupported = fakeHttp((_url, init) => {
      const body = JSON.parse(String(init.body));
      if (body.method === "getSignaturesForAddress") return { result: sigs };
      if (body.params[0] === "s2") return { error: { code: -32015, message: "Transaction version (2) is not supported" } };
      return { result: tx };
    });
    const res = await new Solana(unsupported, "http://sol").poll(owner, "s0");
    expect(res.events).toHaveLength(4);
    expect(res.cursor).toBe("s3");

    let calls = 0;
    const limited = new Http(new Budget(100), async (_url, init) => {
      const body = JSON.parse(String(init.body));
      if (body.method === "getSignaturesForAddress") return new Response(JSON.stringify({ result: sigs }));
      calls++;
      if (calls > 1) return new Response("too many requests", { status: 429 });
      return new Response(JSON.stringify({ result: tx }));
    });
    const partial = await new Solana(limited, "http://sol").poll(owner, "s0");
    expect(partial.events).toHaveLength(2);
    expect(partial.cursor).toBe("s1");
  }, 20_000);
});
