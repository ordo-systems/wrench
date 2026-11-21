import type { Attempt, ClientKey, CostState, PauseReason } from "./api";

// Unreported values render as an em dash so they are never mistaken for zero.
export const missing = "—";

export function tokens(n: number | null): string {
  return n === null ? missing : n.toLocaleString("en-US");
}

export function cost(value: string | null, state: CostState = "confirmed"): string {
  if (state === "pending" || value === null) return "pending";
  return `$${value}`;
}

export function duration(from: string, to: string | null): string {
  if (to === null) return missing;
  const ms = Date.parse(to) - Date.parse(from);
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`;
}

export function outcome(a: Attempt): string {
  if (a.outcome === "") return "running";
  return a.error_reason ? `${a.outcome}: ${a.error_reason}` : a.outcome;
}

export function time(iso: string): string {
  return new Date(iso).toISOString().replace("T", " ").slice(0, 19) + " UTC";
}

const monthNames = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/** compactTime renders a UTC time without the year and seconds ("Nov 19, 22:51") for narrow columns. */
export function compactTime(iso: string): string {
  const d = new Date(iso);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${monthNames[d.getUTCMonth()]} ${d.getUTCDate()}, ${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}`;
}

/** optionalTime renders an absent timestamp as missing. */
export function optionalTime(iso: string | null): string {
  return iso === null ? missing : time(iso);
}

export function keyState(k: ClientKey, now: number = Date.now()): "active" | "revoked" | "expired" {
  if (k.revoked_at !== null) return "revoked";
  return Date.parse(k.expires_at) <= now ? "expired" : "active";
}

/** nanos parses a decimal dollar amount ("0.0004215") into exact nano-dollars. */
export function nanos(amount: string): bigint {
  const [whole, frac = ""] = amount.split(".");
  return BigInt(whole || "0") * 1_000_000_000n + BigInt((frac + "000000000").slice(0, 9));
}

/** dollarsOf formats nano-dollars like the server does: no trailing zeros. */
export function dollarsOf(n: bigint): string {
  const whole = n / 1_000_000_000n;
  const frac = (n % 1_000_000_000n).toString().padStart(9, "0").replace(/0+$/, "");
  return `$${whole}${frac ? `.${frac}` : ""}`;
}

/** cents rounds nano-dollars half up to whole cents ("$195.81") for figures read at a glance. */
export function cents(n: bigint): string {
  const c = (n + 5_000_000n) / 10_000_000n;
  return `$${c / 100n}.${(c % 100n).toString().padStart(2, "0")}`;
}

/** dollars renders a decimal amount ("0.0589218") in cents ("$0.06"); spend above zero but below a cent reads
 * "<$0.01" so it never looks free, and an absent amount is missing. */
export function dollars(amount: string | null | undefined): string {
  if (amount === null || amount === undefined) return missing;
  const n = nanos(amount);
  return n > 0n && n < 10_000_000n ? "<$0.01" : cents(n);
}

/** price renders a catalog price ("0.3" dollars per 1M tokens) with at least two decimals and no trailing zeros
 * beyond them ("$0.30", "$0.075"); an unknown price is missing. */
export function price(amount: string | null): string {
  if (amount === null) return missing;
  const [whole, fraction = ""] = amount.split(".");
  return `$${whole}.${fraction.replace(/0+$/, "").padEnd(2, "0")}`;
}

/** average divides a confirmed amount by a count, rounding down to a nano-dollar; no count is missing data. */
export function average(amount: string, count: number): string {
  return count > 0 ? dollarsOf(nanos(amount) / BigInt(count)) : missing;
}

/** share renders numerator/denominator as a percentage, or missing without a denominator. */
export function share(numerator: number, denominator: number): string {
  return denominator > 0 ? `${Math.round((numerator * 100) / denominator)}%` : missing;
}

/** usageOf renders a user's window as "spent of limit (percent)". */
export function usageOf(
  u: { limits: Record<string, string>; usage: { window: string; spent: string; percent: number }[] },
  window: string,
): string {
  const w = u.usage.find((x) => x.window === window);
  const limit = cost(u.limits[window] ?? null);
  return w ? `${cost(w.spent)} of ${limit} (${w.percent}%)` : `limit ${limit}`;
}

export type HealthTone = "ok" | "warn" | "bad" | "muted";

const windowNames: Record<string, string> = {
  "5_hours": "5 hours",
  day: "day",
  week: "week",
  month: "month",
};

/** windowName renders a budget window token ("5_hours") in words ("5 hours"). */
export function windowName(w: string): string {
  return windowNames[w] ?? w.replace(/_/g, " ");
}

/** ms renders a latency: milliseconds below a second, seconds with one decimal above. */
export function ms(v: number | null): string {
  if (v === null) return missing;
  return v >= 1000 ? `${(v / 1000).toFixed(1)} s` : `${Math.round(v)} ms`;
}

const minuteMs = 60_000;
const hourMs = 60 * minuteMs;
const dayMs = 24 * hourMs;

const yearMs = 365 * dayMs;
const days = (n: number) => `${n} ${n === 1 ? "day" : "days"}`;

/** span renders a non-negative span in its largest whole unit: "5 min", "3 h", "2 days", "1 year". */
function span(ms: number): string {
  if (ms < hourMs) return `${Math.floor(ms / minuteMs)} min`;
  if (ms < dayMs) return `${Math.floor(ms / hourMs)} h`;
  if (ms < yearMs) return days(Math.floor(ms / dayMs));
  const years = Math.floor(ms / yearMs);
  if (years >= 100) return "over 100 years";
  return `${years} ${years === 1 ? "year" : "years"}`;
}

/** relative renders a time against now: "just now" within a minute, else "5 min ago" or "in 3 days". */
export function relative(iso: string, now: number = Date.now()): string {
  const diff = Date.parse(iso) - now;
  if (Math.abs(diff) < minuteMs) return "just now";
  return diff < 0 ? `${span(-diff)} ago` : `in ${span(diff)}`;
}

/** contextSize renders a context window compactly: "128K" below a million, "1M" or "1.5M" above. */
export function contextSize(n: number | null): string {
  if (n === null) return missing;
  if (n >= 1_000_000) return `${Number((n / 1_000_000).toFixed(1))}M`;
  if (n >= 1_000) return `${Math.round(n / 1_000)}K`;
  return String(n);
}

/** resetsIn counts down to a reset: "in 2h 30m", "in 15m", "in 3d 4h"; a past time is "now". */
export function resetsIn(iso: string, now: number = Date.now()): string {
  const ms = Date.parse(iso) - now;
  if (ms <= 0) return "now";
  if (ms < minuteMs) return "in under a minute";
  const d = Math.floor(ms / dayMs);
  const h = Math.floor((ms % dayMs) / hourMs);
  const m = Math.floor((ms % hourMs) / minuteMs);
  if (d > 0) return `in ${d}d ${h}h`;
  return h > 0 ? `in ${h}h ${m}m` : `in ${m}m`;
}

/** age renders the time between two instants: "42 min", "3 h 05 min", then whole days. */
export function age(from: string, to: string): string {
  const ms = Math.max(0, Date.parse(to) - Date.parse(from));
  if (ms < hourMs || ms >= dayMs) return span(ms);
  const m = Math.floor((ms % hourMs) / minuteMs);
  return `${Math.floor(ms / hourMs)} h ${String(m).padStart(2, "0")} min`;
}

/** pauseReason explains why inference is paused. */
export function pauseReason(r: PauseReason): string {
  switch (r.code) {
    case "storage_unavailable":
      return "Accounting storage is unavailable.";
    case "upstream_unavailable":
      return "The model provider is unavailable.";
    case "global_budget_exhausted":
      return "The shared budget is exhausted for now.";
    case "own_budget_exhausted":
      return "Your budget is exhausted for now.";
    case "gitlab_status_unconfirmed":
      return "Your GitLab status is not confirmed.";
  }
}

/** The opencode device login command for a Wrench server origin. */
export function deviceLoginCommand(origin: string): string {
  return `opencode auth login opencode --method device --answer server=${origin}`;
}
