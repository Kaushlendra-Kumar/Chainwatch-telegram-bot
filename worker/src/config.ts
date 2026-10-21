import { Bitcoin } from "./chain/bitcoin";
import { Registry, type Tracker } from "./chain/chain";
import { EVM, EVM_NETWORKS, Etherscan } from "./chain/evm";
import { Solana } from "./chain/solana";
import type { Http } from "./http";
import type { Logger } from "./log";

export interface Env {
  DB: D1Database;
  TELEGRAM_BOT_TOKEN: string;
  WEBHOOK_SECRET?: string;
  ETHERSCAN_API_KEY?: string;
  COINGECKO_API_KEY?: string;
  SOLANA_RPC_URL?: string;
  EVM_CHAINS?: string;
  BITCOIN_API_URL?: string;
  BINANCE_BASE_URL?: string;
  COINGECKO_BASE_URL?: string;
  TELEGRAM_API_URL?: string;
  MAX_ALERTS_PER_USER?: string;
  MAX_WALLETS_PER_USER?: string;
  NOTIFY_BATCH_LIMIT?: string;
  EVENT_RETENTION_DAYS?: string;
  LOG_LEVEL?: string;
}

export interface Config {
  botToken: string;
  webhookSecret: string;
  telegramAPI: string;
  etherscanKey: string;
  evmChains: string[];
  coingeckoKey: string;
  binanceURL: string;
  coingeckoURL: string;
  bitcoinURL: string;
  solanaRPC: string;
  maxAlerts: number;
  maxWallets: number;
  notifyBatchLimit: number;
  retentionDays: number;
  logLevel: string;
}

const str = (v: string | undefined, def: string) => (v && v.trim() !== "" ? v.trim() : def);
const trimSlash = (s: string) => s.replace(/\/+$/, "");

function int(v: string | undefined, def: number): number {
  const n = Number(str(v, ""));
  return Number.isInteger(n) && n >= 0 && str(v, "") !== "" ? n : def;
}

export function loadConfig(env: Env): Config {
  return {
    botToken: str(env.TELEGRAM_BOT_TOKEN, ""),
    webhookSecret: str(env.WEBHOOK_SECRET, ""),
    telegramAPI: trimSlash(str(env.TELEGRAM_API_URL, "https://api.telegram.org")),
    etherscanKey: str(env.ETHERSCAN_API_KEY, ""),
    evmChains: str(env.EVM_CHAINS, "eth,polygon,arbitrum")
      .split(",")
      .map((s) => s.trim().toLowerCase())
      .filter(Boolean),
    coingeckoKey: str(env.COINGECKO_API_KEY, ""),
    binanceURL: trimSlash(str(env.BINANCE_BASE_URL, "https://data-api.binance.vision")),
    coingeckoURL: trimSlash(str(env.COINGECKO_BASE_URL, "https://api.coingecko.com/api/v3")),
    bitcoinURL: trimSlash(str(env.BITCOIN_API_URL, "https://mempool.space/api")),
    solanaRPC: str(env.SOLANA_RPC_URL, "https://api.mainnet-beta.solana.com"),
    maxAlerts: int(env.MAX_ALERTS_PER_USER, 20),
    maxWallets: int(env.MAX_WALLETS_PER_USER, 10),
    notifyBatchLimit: Math.max(1, int(env.NOTIFY_BATCH_LIMIT, 5)),
    retentionDays: Math.max(1, int(env.EVENT_RETENTION_DAYS, 14)),
    logLevel: str(env.LOG_LEVEL, "info"),
  };
}

// The value Telegram sends back in X-Telegram-Bot-Api-Secret-Token. Derived
// from WEBHOOK_SECRET (or the bot token) so it only uses allowed characters.
export async function telegramSecret(cfg: Config): Promise<string> {
  const seed = cfg.webhookSecret || cfg.botToken;
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode("chainwatch:" + seed));
  return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

export function buildChains(cfg: Config, http: Http, log: Logger): Registry {
  const trackers: Tracker[] = [new Bitcoin(http, cfg.bitcoinURL), new Solana(http, cfg.solanaRPC)];
  if (!cfg.etherscanKey) {
    log.debug("ETHERSCAN_API_KEY not set: EVM wallet tracking disabled");
  } else {
    const api = new Etherscan(http, cfg.etherscanKey);
    for (const id of cfg.evmChains) {
      const net = EVM_NETWORKS[id];
      if (!net) {
        log.warn("unknown EVM chain in EVM_CHAINS", { chain: id });
        continue;
      }
      trackers.push(new EVM(net, api));
    }
  }
  return new Registry(...trackers);
}
