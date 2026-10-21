export class NotFoundError extends Error {
  constructor(message = "not found") {
    super(message);
    this.name = "NotFoundError";
  }
}

export class InvalidAddressError extends Error {
  constructor() {
    super("invalid address");
    this.name = "InvalidAddressError";
  }
}

// Thrown when an invocation has used up its share of the free plan's
// 50 outbound requests.
export class BudgetExhaustedError extends Error {
  constructor() {
    super("subrequest budget exhausted");
    this.name = "BudgetExhaustedError";
  }
}

export class RecipientGoneError extends Error {
  constructor(detail: string) {
    super(`recipient unavailable: ${detail}`);
    this.name = "RecipientGoneError";
  }
}

export class StatusError extends Error {
  constructor(
    readonly code: number,
    readonly body: string,
  ) {
    super(`http ${code}: ${body}`);
    this.name = "StatusError";
  }
}

export function isStatus(err: unknown, code: number): boolean {
  return err instanceof StatusError && err.code === code;
}

export function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
