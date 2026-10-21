import { BudgetExhaustedError, StatusError } from "./errors";

export const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

// Counts outbound fetches for one invocation, so a cron run or a webhook
// update stays under the free plan's subrequest limit.
export class Budget {
  used = 0;

  constructor(readonly limit: number) {}

  get remaining(): number {
    return Math.max(0, this.limit - this.used);
  }

  take(): void {
    if (this.used >= this.limit) {
      throw new BudgetExhaustedError();
    }
    this.used++;
  }
}

// Spaces calls evenly. Instances are module-level, so the spacing holds for
// everything running in the same isolate.
export class Limiter {
  private next = 0;
  private readonly interval: number;

  constructor(perSecond: number) {
    this.interval = perSecond > 0 ? 1000 / perSecond : 0;
  }

  async wait(): Promise<void> {
    if (!this.interval) {
      return;
    }
    const now = Date.now();
    if (this.next < now) {
      this.next = now;
    }
    const wait = this.next - now;
    this.next += this.interval;
    if (wait > 0) {
      await sleep(wait);
    }
  }
}

export interface RequestOptions {
  timeoutMs?: number;
  limiter?: Limiter;
  headers?: Record<string, string>;
  retries?: number;
}

export type FetchFn = (url: string, init: RequestInit) => Promise<Response>;

export class Http {
  constructor(
    readonly budget: Budget,
    // Wrapped so fetch is never called with a foreign `this`.
    private readonly fetchFn: FetchFn = (url, init) => fetch(url, init),
  ) {}

  async send(url: string, init: RequestInit, opts: RequestOptions = {}): Promise<Response> {
    await opts.limiter?.wait();
    this.budget.take();
    return this.fetchFn(url, { ...init, signal: AbortSignal.timeout(opts.timeoutMs ?? 10_000) });
  }

  async bytes(method: string, url: string, payload: unknown, opts: RequestOptions = {}): Promise<ArrayBuffer> {
    const headers: Record<string, string> = {
      Accept: "application/json",
      "User-Agent": "chainwatch-bot/1.0",
      ...opts.headers,
    };
    let body: string | undefined;
    if (payload !== undefined) {
      headers["Content-Type"] = "application/json";
      body = JSON.stringify(payload);
    }
    const retries = opts.retries ?? 2;
    let last: unknown;
    for (let attempt = 0; attempt <= retries; attempt++) {
      if (attempt > 0) {
        await sleep(attempt * attempt * 700);
      }
      let res: Response;
      try {
        res = await this.send(url, { method, headers, body }, opts);
      } catch (err) {
        if (err instanceof BudgetExhaustedError) {
          throw err;
        }
        last = err;
        continue;
      }
      const buf = await res.arrayBuffer();
      if (res.ok) {
        return buf;
      }
      last = new StatusError(res.status, new TextDecoder().decode(buf.slice(0, 300)));
      if (res.status !== 429 && res.status < 500) {
        break;
      }
    }
    throw last;
  }

  async text(url: string, opts?: RequestOptions): Promise<string> {
    return new TextDecoder().decode(await this.bytes("GET", url, undefined, opts)).trim();
  }

  async json<T>(url: string, opts?: RequestOptions): Promise<T> {
    return decode<T>(await this.bytes("GET", url, undefined, opts));
  }

  async postJSON<T>(url: string, payload: unknown, opts?: RequestOptions): Promise<T> {
    return decode<T>(await this.bytes("POST", url, payload, opts));
  }
}

function decode<T>(buf: ArrayBuffer): T {
  const text = new TextDecoder().decode(buf);
  try {
    return JSON.parse(text) as T;
  } catch (err) {
    throw new Error(`decode response: ${(err as Error).message} (body: ${text.slice(0, 200)})`);
  }
}
