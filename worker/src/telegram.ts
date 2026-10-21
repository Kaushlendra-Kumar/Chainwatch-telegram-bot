import { RecipientGoneError } from "./errors";
import { sleep, type Http } from "./http";

export interface TgUser {
  id: number;
  is_bot: boolean;
  first_name: string;
  username?: string;
}

export interface TgMessage {
  message_id: number;
  from?: TgUser;
  chat: { id: number; type: string };
  text?: string;
}

export interface TgUpdate {
  update_id: number;
  message?: TgMessage;
}

export class TelegramError extends Error {
  constructor(
    readonly code: number,
    readonly description: string,
    readonly retryAfter = 0,
  ) {
    super(`telegram ${code}: ${description}`);
    this.name = "TelegramError";
  }
}

export interface SendOptions {
  noPreview?: boolean;
}

export class Telegram {
  constructor(
    private readonly http: Http,
    private readonly token: string,
    private readonly api = "https://api.telegram.org",
  ) {}

  async call<T = unknown>(method: string, payload: Record<string, unknown> | FormData = {}): Promise<T> {
    const form = payload instanceof FormData;
    const res = await this.http.send(
      `${this.api}/bot${this.token}/${method}`,
      {
        method: "POST",
        headers: form ? undefined : { "Content-Type": "application/json" },
        body: form ? payload : JSON.stringify(payload),
      },
      { timeoutMs: 30_000 },
    );
    const data = (await res.json().catch(() => null)) as {
      ok?: boolean;
      result?: T;
      error_code?: number;
      description?: string;
      parameters?: { retry_after?: number };
    } | null;
    if (!data?.ok) {
      throw new TelegramError(data?.error_code ?? res.status, data?.description ?? `http ${res.status}`, data?.parameters?.retry_after ?? 0);
    }
    return data.result as T;
  }

  sendMessage(chatId: number, html: string, opts: SendOptions = {}): Promise<unknown> {
    return this.call("sendMessage", {
      chat_id: chatId,
      text: html,
      parse_mode: "HTML",
      ...(opts.noPreview ? { link_preview_options: { is_disabled: true } } : {}),
    });
  }

  async sendChatAction(chatId: number, action: "typing" | "upload_photo"): Promise<void> {
    await this.call("sendChatAction", { chat_id: chatId, action }).catch(() => undefined);
  }

  sendPhoto(chatId: number, png: ArrayBuffer, caption: string): Promise<unknown> {
    const form = new FormData();
    form.set("chat_id", String(chatId));
    form.set("caption", caption);
    form.set("parse_mode", "HTML");
    form.set("photo", new Blob([png], { type: "image/png" }), "chart.png");
    return this.call("sendPhoto", form);
  }

  // Used by the cron jobs: waits out one flood limit and reports chats that
  // can no longer be reached.
  async notify(chatId: number, html: string): Promise<void> {
    for (let attempt = 0; attempt < 2; attempt++) {
      try {
        await this.sendMessage(chatId, html, { noPreview: true });
        return;
      } catch (err) {
        if (!(err instanceof TelegramError)) throw err;
        if (err.code === 429 && attempt === 0) {
          await sleep(Math.min(Math.max(err.retryAfter, 1), 30) * 1000);
          continue;
        }
        if (isGone(err)) throw new RecipientGoneError(err.description);
        throw err;
      }
    }
    throw new Error("telegram flood limit");
  }
}

function isGone(err: TelegramError): boolean {
  if (err.code === 403) return true;
  const d = err.description.toLowerCase();
  return err.code === 400 && (d.includes("chat not found") || d.includes("user is deactivated"));
}

export const commandMenu = [
  { command: "price", description: "Current price: /price btc eth" },
  { command: "chart", description: "Price chart: /chart btc 7d" },
  { command: "top", description: "Top 10 coins by market cap" },
  { command: "alert", description: "Price alert: /alert btc above 100000" },
  { command: "alerts", description: "Your active price alerts" },
  { command: "watch", description: "Watch a wallet: /watch eth 0x… label" },
  { command: "wallets", description: "Wallets you are watching" },
  { command: "balance", description: "Wallet balance: /balance sol <address>" },
  { command: "portfolio", description: "Your holdings and P&L" },
  { command: "add", description: "Record a buy: /add btc 0.5 60000" },
  { command: "help", description: "All commands" },
];
