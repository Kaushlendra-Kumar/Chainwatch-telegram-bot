import { message } from "./errors";

const levels = { debug: 0, info: 1, warn: 2, error: 3 } as const;
type Level = keyof typeof levels;

export interface Logger {
  debug(msg: string, fields?: Record<string, unknown>): void;
  info(msg: string, fields?: Record<string, unknown>): void;
  warn(msg: string, fields?: Record<string, unknown>): void;
  error(msg: string, fields?: Record<string, unknown>): void;
}

export function newLogger(level = "info", base: Record<string, unknown> = {}): Logger {
  const min = levels[(level.toLowerCase() as Level) in levels ? (level.toLowerCase() as Level) : "info"];
  const write = (lvl: Level) => (msg: string, fields: Record<string, unknown> = {}) => {
    if (levels[lvl] < min) {
      return;
    }
    const out: Record<string, unknown> = { level: lvl, msg, ...base };
    for (const [k, v] of Object.entries(fields)) {
      out[k] = v instanceof Error ? message(v) : v;
    }
    const line = JSON.stringify(out);
    if (lvl === "error") console.error(line);
    else if (lvl === "warn") console.warn(line);
    else console.log(line);
  };
  return { debug: write("debug"), info: write("info"), warn: write("warn"), error: write("error") };
}

export const quietLogger: Logger = { debug() {}, info() {}, warn() {}, error() {} };
