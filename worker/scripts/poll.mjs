// Local development without a public URL: pulls updates for the DEV bot with
// getUpdates and forwards each one to `npm run dev` as if Telegram had sent it.
// Usage: npm run dev (one terminal), npm run poll (another).
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";

const vars = Object.fromEntries(
  readFileSync(new URL("../.dev.vars", import.meta.url), "utf8")
    .split(/\r?\n/)
    .filter((l) => l.trim() && !l.trim().startsWith("#") && l.includes("="))
    .map((l) => [l.slice(0, l.indexOf("=")).trim(), l.slice(l.indexOf("=") + 1).trim()]),
);
const token = vars.TELEGRAM_BOT_TOKEN;
if (!token) throw new Error("TELEGRAM_BOT_TOKEN missing in .dev.vars");
const secret = createHash("sha256").update("chainwatch:" + (vars.WEBHOOK_SECRET || token)).digest("hex");
const local = process.env.LOCAL_URL ?? "http://127.0.0.1:8788/telegram/webhook";
const api = (method, body) =>
  fetch(`https://api.telegram.org/bot${token}/${method}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body ?? {}),
  }).then((r) => r.json());

await api("deleteWebhook");
console.log(`forwarding updates to ${local} (Ctrl+C to stop)`);
let offset = 0;
for (;;) {
  const res = await api("getUpdates", { offset, timeout: 30, allowed_updates: ["message"] });
  if (!res.ok) {
    console.error(res.description);
    await new Promise((r) => setTimeout(r, 3000));
    continue;
  }
  for (const u of res.result) {
    offset = u.update_id + 1;
    const r = await fetch(local, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Telegram-Bot-Api-Secret-Token": secret },
      body: JSON.stringify(u),
    }).catch((err) => ({ status: String(err) }));
    console.log(`update ${u.update_id}: ${u.message?.text ?? "(no text)"} -> ${r.status}`);
  }
}
