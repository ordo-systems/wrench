// The six usage cards and the chart data behind them. Everything here is pure: widgets pass Stats in and hand
// the result to Chart.vue or ActivityGrid.vue, so tests check the numbers a chart gets rather than how it is drawn.
// Unreported values stay null (gaps in a chart, an em dash on a card); they are never drawn as zero.
import type { EChartsCoreOption } from "echarts/core";
import type { Period, Stats, StatsBucket } from "./api";
import * as fmt from "./format";

export type MetricId = "spend" | "tokens" | "attempts" | "first-token" | "cache" | "sessions";

export interface Metric {
  id: MetricId;
  title: string;
  /** The chart shape of the detail chart's series that do not choose their own. */
  kind: "line" | "bar";
  /** Additive metrics sum over users, so the by-user view stacks them and adds the others. */
  additive: boolean;
  /** Axis and tooltip formatting of one plotted value. */
  format: (v: number) => string;
  note: string;
}

/** Series is one plotted line or bar run; color names a theme token such as "--bad". */
export interface Series {
  name: string;
  values: (number | null)[];
  color?: string;
  stack?: boolean;
  kind?: "line" | "bar";
}

/** compact formats axis numbers: 1200 -> 1.2k, 3500000 -> 3.5M. */
export function compact(v: number): string {
  const a = Math.abs(v);
  if (a >= 1e6) return `${+(v / 1e6).toFixed(1)}M`;
  if (a >= 1e3) return `${+(v / 1e3).toFixed(1)}k`;
  if (a > 0 && a < 1) return `${+v.toPrecision(2)}`;
  return `${+v.toFixed(1)}`;
}

const usd = (v: number) => `$${compact(v)}`;
const percent = (v: number) => `${Math.round(v)}%`;

export const metrics: Metric[] = [
  {
    id: "spend",
    title: "Confirmed spend",
    kind: "bar",
    additive: true,
    format: usd,
    note: "Attempts with an unknown cost are not included until they are confirmed.",
  },
  {
    id: "tokens",
    title: "Tokens",
    kind: "bar",
    additive: true,
    format: compact,
    note: "Input plus output; reasoning is part of output where the provider reports it.",
  },
  { id: "attempts", title: "LLM requests", kind: "bar", additive: true, format: compact, note: "" },
  {
    id: "first-token",
    title: "Time to first token",
    kind: "line",
    additive: false,
    format: (v) => fmt.ms(v),
    note: "p95 over the period; buckets without observations are gaps.",
  },
  {
    id: "cache",
    title: "Cached share of input",
    kind: "line",
    additive: false,
    format: percent,
    note: "Only attempts that report both input and cache-read tokens count.",
  },
  {
    id: "sessions",
    title: "Active sessions",
    kind: "line",
    additive: true,
    format: compact,
    note: "From requests in 5-minute steps; the model filter does not apply.",
  },
];

export const periods: { value: Period; label: string }[] = [
  { value: "24h", label: "Last 24 hours" },
  { value: "7d", label: "Last 7 days" },
  { value: "30d", label: "Last 30 days" },
];

/** periodOf reads the period from a route query value; anything else is the default, 24 hours. */
export function periodOf(v: unknown): Period {
  return periods.some((p) => p.value === v) ? (v as Period) : "24h";
}

export function isMetric(s: string): s is MetricId {
  return metrics.some((m) => m.id === s);
}

export function metric(id: MetricId): Metric {
  return metrics.find((m) => m.id === id) as Metric;
}

// sum adds the reported parts; nothing reported is null, not zero.
function sum(...parts: (number | null)[]): number | null {
  const known = parts.filter((p): p is number => p !== null);
  return known.length ? known.reduce((a, b) => a + b, 0) : null;
}

const cacheShare = (b: StatsBucket) =>
  b.cacheable_input > 0 ? (b.cached_input * 100) / b.cacheable_input : null;

// point is the card's value in one bucket: what an activity cell and a per-user series show.
function point(id: MetricId, b: StatsBucket): number | null {
  switch (id) {
    case "spend":
      return Number(b.cost);
    case "tokens":
      return sum(b.input_tokens.sum, b.output_tokens.sum);
    case "attempts":
      return b.attempts;
    case "first-token":
      return b.first_token.p95_ms;
    case "cache":
      return cacheShare(b);
    case "sessions":
      return b.active_sessions ?? null;
  }
}

/**
 * cardValue is the headline of a card: the period total, or the latest bucket for active sessions. Without
 * attempts in the period every card is missing; with attempts each shows its value, zero included, and only a
 * percentile without observations stays missing.
 */
export function cardValue(id: MetricId, stats: Stats): string {
  const t = stats.total;
  if (t.attempts === 0) return fmt.missing;
  switch (id) {
    case "spend":
      return fmt.cents(fmt.nanos(t.cost));
    case "tokens":
      return fmt.tokens(sum(t.input_tokens.sum, t.output_tokens.sum) ?? 0);
    case "attempts":
      return String(t.attempts);
    case "first-token":
      return fmt.ms(t.first_token.p95_ms);
    case "cache":
      return t.cacheable_input > 0 ? fmt.share(t.cached_input, t.cacheable_input) : "0%";
    case "sessions":
      return String(stats.buckets.at(-1)?.active_sessions ?? 0);
  }
}

export interface Fact {
  label: string;
  value: string;
  /** The exact value, when value is rounded. */
  title?: string;
  /** A faint line under the value, such as how many attempts reported it. */
  note?: string;
}

const reported = (o: { sum: number | null; reported: number }, attempts: number) => ({
  value: fmt.tokens(o.sum),
  note: `reported by ${o.reported} of ${attempts}`,
});

/** facts are the period figures listed under a metric's detail chart. */
export function facts(id: MetricId, stats: Stats): Fact[] {
  const t = stats.total;
  const s = stats.sessions;
  // Money reads in cents with the exact amount in the title, as in the sessions table.
  const money = (v: string | null): { value: string; title?: string } =>
    v === null ? { value: fmt.missing } : { value: fmt.dollars(v), title: fmt.cost(v) };
  const averaged = fmt.average(t.cost, t.confirmed);
  const perRequest = money(averaged === fmt.missing ? null : averaged.slice(1));
  switch (id) {
    case "spend":
      return [
        { label: "Confirmed spend", ...money(t.cost) },
        { label: "Attempts with unknown cost", value: String(t.pending) },
        { label: "Oldest unknown cost", value: fmt.optionalTime(stats.oldest_pending) },
        { label: "Average per request", ...perRequest },
        { label: "Sessions", value: String(s.sessions), note: `${s.incomplete} incomplete` },
        { label: "Session average", ...money(s.average) },
        { label: "Session p50", ...money(s.p50) },
        { label: "Session p95", ...money(s.p95) },
      ];
    case "tokens":
      return [
        { label: "Input", ...reported(t.input_tokens, t.attempts) },
        { label: "Output", ...reported(t.output_tokens, t.attempts) },
        { label: "Reasoning", ...reported(t.reasoning_tokens, t.attempts) },
        { label: "Cache read", ...reported(t.cache_read_tokens, t.attempts) },
      ];
    case "attempts":
      return [
        { label: "Attempts", value: String(t.attempts) },
        { label: "Requests", value: String(t.requests) },
        { label: "Extra attempts", value: String(t.attempts - t.requests) },
        { label: "Errors", value: String(t.errors) },
        { label: "Cancelled", value: String(t.cancelled) },
      ];
    case "first-token":
      return [
        { label: "p50", value: fmt.ms(t.first_token.p50_ms) },
        { label: "p95", value: fmt.ms(t.first_token.p95_ms) },
        { label: "Observed attempts", value: String(t.first_token.observed) },
      ];
    case "cache":
      return [
        { label: "Cached share", value: fmt.share(t.cached_input, t.cacheable_input) },
        { label: "Cached input tokens", value: fmt.tokens(t.cached_input) },
        { label: "Cacheable input tokens", value: fmt.tokens(t.cacheable_input) },
      ];
    case "sessions": {
      const known = stats.buckets.map((b) => b.active_sessions).filter((v): v is number => v !== undefined);
      const latest = stats.buckets.at(-1)?.active_sessions;
      return [
        { label: "Latest", value: latest === undefined ? fmt.missing : String(latest) },
        { label: "Peak", value: known.length ? String(Math.max(...known)) : fmt.missing },
      ];
    }
  }
}

/**
 * cellValues are the card value per bucket, the cells of its activity grid. Like the card, the cached share is
 * zero in a bucket with attempts but no cacheable input, so such a bucket reads 0% rather than no data.
 */
export function cellValues(id: MetricId, stats: Stats): (number | null)[] {
  return stats.buckets.map((b) => (id === "cache" && b.attempts > 0 ? (cacheShare(b) ?? 0) : point(id, b)));
}

/** cardSeries are the series of a metric's detail chart. */
export function cardSeries(id: MetricId, stats: Stats): Series[] {
  const of = (f: (b: StatsBucket) => number | null) => stats.buckets.map(f);
  switch (id) {
    case "spend":
      return [{ name: "Confirmed cost", values: of((b) => Number(b.cost)) }];
    case "tokens":
      return [
        { name: "Input", values: of((b) => b.input_tokens.sum), stack: true },
        { name: "Output", values: of((b) => b.output_tokens.sum), stack: true },
        { name: "Reasoning", values: of((b) => b.reasoning_tokens.sum), kind: "line" },
      ];
    case "attempts":
      return [
        { name: "Success", values: of((b) => b.success), color: "--ok", stack: true },
        { name: "Errors", values: of((b) => b.errors), color: "--bad", stack: true },
        { name: "Cancelled", values: of((b) => b.cancelled), color: "--warn", stack: true },
      ];
    case "first-token":
      return [
        { name: "p50", values: of((b) => b.first_token.p50_ms) },
        { name: "p95", values: of((b) => b.first_token.p95_ms) },
      ];
    case "cache":
      return [{ name: "Cached share", values: of(cacheShare) }];
    case "sessions":
      return [{ name: "Active sessions", values: of((b) => b.active_sessions ?? null) }];
  }
}

/**
 * level is the intensity of an activity cell from 0 to 4: 0 for no data, zero or a period without a positive
 * maximum, otherwise the quarter of the maximum the value falls in (up to 25% is 1, up to 50% is 2, up to 75% is
 * 3, above is 4), so any positive value is visible.
 */
export function level(value: number | null, max: number): 0 | 1 | 2 | 3 | 4 {
  if (value === null || value <= 0 || max <= 0) return 0;
  return Math.min(4, Math.max(1, Math.ceil((value / max) * 4))) as 1 | 2 | 3 | 4;
}

/** One bucket of an activity grid: its UTC start and the card value, null when nothing was reported. */
export interface ActivityBucket {
  at: string;
  value: number | null;
}

/** One cell of an activity grid; a blank cell pads the layout and stands for no time of the period. */
export interface ActivityCell {
  row: number;
  col: number;
  blank: boolean;
  level: 0 | 1 | 2 | 3 | 4;
  title?: string;
}

const hourMs = 3_600_000;
const dayMs = 24 * hourMs;

interface Slot {
  /** The bucket start the cell shows; null for a blank cell. */
  t: number | null;
  row: number;
  col: number;
}

// slotCounts is how many cells each period draws in its one row, and how wide each cell is.
const slotCounts: Record<Period, [number, number]> = {
  "24h": [24, hourMs],
  "7d": [168, hourMs],
  "30d": [30, dayMs],
};

// slotsOf positions the cells of a period that ends with the bucket starting at end (see activityLayout).
function slotsOf(period: Period, end: number): Slot[] {
  const [count, width] = slotCounts[period];
  return Array.from({ length: count }, (_, i) => ({ t: end - (count - 1 - i) * width, row: 0, col: i }));
}

/**
 * activityLayout places a period's buckets in one row of cells, anchored at the last bucket and in UTC like the
 * bucket starts: the last 24 hours, the last 168 hours or the last 30 days. Levels step over the largest value
 * shown; times without a bucket read "no data".
 */
export function activityLayout(
  buckets: ActivityBucket[],
  period: Period,
  format: (v: number) => string = compact,
): { rows: number; cols: number; cells: ActivityCell[] } {
  if (!buckets.length) return { rows: 0, cols: 0, cells: [] };
  const values = new Map(buckets.map((b) => [Date.parse(b.at), b.value]));
  const slots = slotsOf(period, Math.max(...values.keys()));
  const shown = slots.map((s) => (s.t === null ? null : (values.get(s.t) ?? null)));
  const max = Math.max(0, ...shown.filter((v): v is number => v !== null));
  const when = (t: number) => {
    const iso = new Date(t).toISOString();
    return period === "30d" ? iso.slice(0, 10) : `${iso.slice(0, 16).replace("T", " ")} UTC`;
  };
  const cells = slots.map(({ t, row, col }, i): ActivityCell => {
    if (t === null) return { row, col, blank: true, level: 0 };
    const v = shown[i] ?? null;
    return {
      row,
      col,
      blank: false,
      level: level(v, max),
      title: `${when(t)}: ${v === null ? "no data" : format(v)}`,
    };
  });
  return {
    rows: Math.max(...slots.map((s) => s.row)) + 1,
    cols: Math.max(...slots.map((s) => s.col)) + 1,
    cells,
  };
}

/**
 * userSeries is the by-user detail chart (stats requested with group_by=user). Additive metrics stack the top
 * users and the others; percentiles and ratios do not add up, so they draw one line per top user and leave the
 * others out (the server reports their percentile and cache fields as unknown anyway).
 */
export function userSeries(id: MetricId, stats: Stats): Series[] {
  const additive = metric(id).additive;
  return (stats.series ?? [])
    .filter((s) => additive || s.user !== "")
    .map((s) => ({
      name: s.user || "Others",
      values: s.buckets.map((b) => point(id, b)),
      stack: additive,
      kind: additive ? "bar" : "line",
      color: s.user ? undefined : "--muted",
    }));
}

export interface ChartInput {
  starts: string[];
  series: Series[];
  kind: "line" | "bar";
  format?: (v: number) => string;
}

// Bucket starts are UTC; labels drop the year and seconds.
const label = (iso: string) => iso.slice(5, 16).replace("T", " ");

/**
 * chartOption builds the ECharts option of a chart. color resolves a theme token to a color, so both themes
 * work; series without a token take the chart palette in order.
 */
export function chartOption(c: ChartInput, color: (token: string) => string): EChartsCoreOption {
  const format = c.format ?? compact;
  const series = c.series.map((s, i) => {
    const kind = s.kind ?? c.kind;
    const col = color(s.color ?? `--chart-${(i % 8) + 1}`);
    return {
      name: s.name,
      type: kind,
      data: s.values,
      stack: s.stack ? "total" : undefined,
      itemStyle: { color: col },
      lineStyle: { color: col, width: 2 },
      barMaxWidth: 24,
    };
  });
  const xAxis = { type: "category", data: c.starts.map(label) };
  const yAxis = { type: "value", min: 0 };
  const muted = color("--muted");
  const line = color("--line");
  return {
    grid: { left: 64, right: 16, top: 36, bottom: 64 },
    tooltip: {
      trigger: "axis",
      valueFormatter: (v: unknown) => (typeof v === "number" ? format(v) : "no data"),
      backgroundColor: color("--card"),
      borderColor: line,
      textStyle: { color: color("--text") },
    },
    // The legend sits above the grid; the bottom belongs to the zoom slider.
    legend: { show: series.length > 1, type: "scroll", top: 0, textStyle: { color: muted } },
    xAxis: { ...xAxis, axisLine: { lineStyle: { color: line } }, axisLabel: { color: muted } },
    yAxis: {
      ...yAxis,
      splitLine: { lineStyle: { color: line } },
      axisLabel: { color: muted, formatter: format },
    },
    dataZoom: [{ type: "inside" }, { type: "slider", height: 18, bottom: 12, textStyle: { color: muted } }],
    series,
  };
}
