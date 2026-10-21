const htmlEscapes: Record<string, string> = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&#34;", "'": "&#39;" };

export function escape(s: string): string {
  return s.replace(/[&<>"']/g, (c) => htmlEscapes[c]!);
}

export function usd(v: number): string {
  let sign = "";
  if (v < 0) {
    sign = "-";
    v = -v;
  }
  if (v === 0) return "$0.00";
  if (v >= 1) return sign + "$" + withCommas(v.toFixed(2));
  if (v >= 0.01) return sign + "$" + v.toFixed(4);
  return sign + "$" + trimZeros(v.toFixed(10));
}

export function signedUSD(v: number): string {
  return v > 0 ? "+" + usd(v) : usd(v);
}

export function amount(v: number): string {
  let sign = "";
  if (v < 0) {
    sign = "-";
    v = -v;
  }
  if (v === 0) return "0";
  if (v >= 1000) return sign + withCommas(trimZeros(v.toFixed(2)));
  if (v >= 1) return sign + trimZeros(v.toFixed(4));
  return sign + trimZeros(v.toFixed(8));
}

export function percent(v: number): string {
  if (!Number.isFinite(v)) return "n/a";
  const s = v.toFixed(2);
  return (v > 0 ? "+" + s : s) + "%";
}

export function trend(v: number): string {
  if (v > 0) return "🟢";
  if (v < 0) return "🔴";
  return "⚪";
}

export function compactUSD(v: number): string {
  const abs = Math.abs(v);
  if (abs >= 1e12) return `$${(v / 1e12).toFixed(2)}T`;
  if (abs >= 1e9) return `$${(v / 1e9).toFixed(2)}B`;
  if (abs >= 1e6) return `$${(v / 1e6).toFixed(2)}M`;
  if (abs >= 1e3) return `$${(v / 1e3).toFixed(2)}K`;
  return usd(v);
}

export function shortAddr(a: string): string {
  const r = Array.from(a);
  if (r.length <= 14) return a;
  return r.slice(0, 6).join("") + "…" + r.slice(-4).join("");
}

// t and now are unix milliseconds.
export function ago(t: number, now: number): string {
  const d = now - t;
  if (d < 60_000) return "just now";
  if (d < 3_600_000) return `${Math.floor(d / 60_000)}m ago`;
  if (d < 48 * 3_600_000) return `${Math.floor(d / 3_600_000)}h ago`;
  return `${Math.floor(d / 86_400_000)}d ago`;
}

function trimZeros(s: string): string {
  if (!s.includes(".")) return s;
  s = s.replace(/0+$/, "");
  return s.endsWith(".") ? s.slice(0, -1) : s;
}

function withCommas(s: string): string {
  const i = s.indexOf(".");
  const intPart = i >= 0 ? s.slice(0, i) : s;
  const frac = i >= 0 ? s.slice(i) : "";
  return intPart.replace(/\B(?=(\d{3})+(?!\d))/g, ",") + frac;
}
