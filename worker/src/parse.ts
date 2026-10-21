const floatRe = /^[+-]?(\d+\.?\d*|\.\d+)(e[+-]?\d+)?$/;

// Accepts shortcuts like 100k, 1.5m, $2,500. Returns null unless the result
// is a finite positive number.
export function parseNumber(input: string): number | null {
  let s = input.trim().toLowerCase();
  if (s.startsWith("$")) s = s.slice(1);
  s = s.replace(/[,_]/g, "");
  let mult = 1;
  if (s.endsWith("k")) [mult, s] = [1e3, s.slice(0, -1)];
  else if (s.endsWith("m")) [mult, s] = [1e6, s.slice(0, -1)];
  else if (s.endsWith("b")) [mult, s] = [1e9, s.slice(0, -1)];
  if (!floatRe.test(s)) return null;
  const v = Number(s);
  if (!Number.isFinite(v) || v <= 0) return null;
  return v * mult;
}

export function parseDirection(s: string): "above" | "below" | null {
  switch (s.trim().toLowerCase()) {
    case "above":
    case ">":
    case ">=":
    case "over":
    case "up":
    case "gt":
      return "above";
    case "below":
    case "<":
    case "<=":
    case "under":
    case "down":
    case "lt":
      return "below";
    default:
      return null;
  }
}

export function parseID(s: string): number | null {
  const t = s.trim().replace(/^#/, "");
  if (!/^\+?\d+$/.test(t)) return null;
  const id = Number(t);
  return Number.isSafeInteger(id) && id > 0 ? id : null;
}

export function parseDays(s: string): number | null {
  switch (s.trim().toLowerCase()) {
    case "":
    case "7d":
    case "1w":
    case "week":
      return 7;
    case "1d":
    case "24h":
    case "day":
      return 1;
    case "30d":
    case "1m":
    case "month":
      return 30;
    case "90d":
    case "3m":
      return 90;
    case "1y":
    case "365d":
    case "year":
      return 365;
    default:
      return null;
  }
}
