import type { Http } from "./http";
import type { Point } from "./price/types";

const maxPoints = 200;
const months = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

function label(ms: number, days: number): string {
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, "0");
  if (days <= 1) return `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}`;
  return `${months[d.getUTCMonth()]} ${pad(d.getUTCDate())}`;
}

function downsample(points: Point[]): Point[] {
  if (points.length <= maxPoints) return points;
  const step = Math.ceil(points.length / maxPoints);
  const out = points.filter((_, i) => i % step === 0);
  if (out[out.length - 1] !== points[points.length - 1]) out.push(points[points.length - 1]!);
  return out;
}

// Chart.js config for QuickChart. It is sent as a string so the tick
// formatter can be a function.
export function chartConfig(title: string, points: Point[], days: number): string {
  if (points.length < 2) throw new Error("not enough data points");
  const pts = downsample(points);
  const ys = pts.map((p) => p.price);
  const lo = Math.min(...ys);
  const hi = Math.max(...ys);
  const up = ys[ys.length - 1]! >= ys[0]!;
  const color = up ? "#16a34a" : "#dc2626";
  const fill = up ? "rgba(22,163,74,0.16)" : "rgba(220,38,38,0.16)";
  let pad = (hi - lo) * 0.08;
  if (pad === 0) pad = hi * 0.01;

  const config = {
    type: "line",
    data: {
      labels: pts.map((p) => label(p.time, days)),
      datasets: [{ data: ys, borderColor: color, backgroundColor: fill, borderWidth: 2.5, pointRadius: 0, fill: true, tension: 0.1 }],
    },
    options: {
      plugins: { title: { display: true, text: title, font: { size: 16 } }, legend: { display: false } },
      scales: {
        x: { ticks: { maxTicksLimit: 8, maxRotation: 0 }, grid: { color: "#e5e7eb" } },
        y: { min: lo - pad, max: hi + pad, ticks: { callback: "__TICK__" }, grid: { color: "#e5e7eb" } },
      },
    },
  };
  const tick = `(v) => '$' + (Math.abs(v) >= 1 ? v.toLocaleString('en-US', { maximumFractionDigits: 2 }) : Number(v.toPrecision(4)))`;
  // A function replacer, because the formatter contains "$'".
  return JSON.stringify(config).replace(`"__TICK__"`, () => tick);
}

export async function renderChart(http: Http, title: string, points: Point[], days: number): Promise<ArrayBuffer> {
  return http.bytes(
    "POST",
    "https://quickchart.io/chart",
    { version: "4", width: 1100, height: 560, backgroundColor: "#ffffff", format: "png", chart: chartConfig(title, points, days) },
    { timeoutMs: 20_000, retries: 1 },
  );
}
