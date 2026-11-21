import { describe, expect, it } from "vitest";
import type { Stats, StatsBucket } from "./api";
import {
  cardSeries,
  cardValue,
  chartOption,
  facts,
  isMetric,
  level,
  metrics,
  periodOf,
  cellValues,
  userSeries,
} from "./metrics";

const observed = (sum: number | null) => ({ sum, reported: sum === null ? 0 : 1 });

function bucket(start: string, over: Partial<StatsBucket> = {}): StatsBucket {
  return {
    start,
    attempts: 5,
    requests: 4,
    success: 3,
    errors: 1,
    cancelled: 1,
    running: 0,
    pending: 0,
    confirmed: 5,
    cost: "0.5",
    input_tokens: observed(100),
    output_tokens: observed(20),
    reasoning_tokens: observed(null),
    cache_read_tokens: observed(40),
    cache_write_tokens: observed(null),
    cached_input: 40,
    cacheable_input: 100,
    first_token: { observed: 5, p50_ms: 300, p95_ms: 900 },
    duration: { observed: 5, p50_ms: 1000, p95_ms: 2000 },
    active_sessions: 2,
    ...over,
  };
}

const users = Array.from({ length: 8 }, (_, i) => `u${i}@example.com`);

const stats: Stats = {
  buckets: [
    bucket("2026-11-09T10:00:00Z"),
    bucket("2026-11-09T11:00:00Z", {
      first_token: { observed: 0, p50_ms: null, p95_ms: null },
      active_sessions: 3,
    }),
  ],
  total: bucket("", {
    attempts: 10,
    cost: "1.23",
    input_tokens: observed(1000),
    output_tokens: observed(234),
    cached_input: 250,
    cacheable_input: 1000,
    first_token: { observed: 10, p50_ms: 320, p95_ms: 1500 },
  }),
  by_user: [],
  by_model: [],
  by_provider: [],
  sessions: { sessions: 2, incomplete: 0, average: "0.6", p50: "0.5", p95: "0.7" },
  oldest_pending: null,
  series: [
    ...users.map((user) => ({ user, buckets: [bucket("a"), bucket("b")] })),
    {
      user: "",
      buckets: [
        bucket("a", {
          cached_input: 0,
          cacheable_input: 0,
          first_token: { observed: 0, p50_ms: null, p95_ms: null },
        }),
        bucket("b", {
          cached_input: 0,
          cacheable_input: 0,
          first_token: { observed: 0, p50_ms: null, p95_ms: null },
        }),
      ],
    },
  ],
};

describe("metric cards", () => {
  it("lists the six cards in the spec's order", () => {
    expect(metrics.map((m) => m.id)).toEqual([
      "spend",
      "tokens",
      "attempts",
      "first-token",
      "cache",
      "sessions",
    ]);
    expect(isMetric("cache")).toBe(true);
    expect(isMetric("nope")).toBe(false);
  });

  it("renders each card's current value", () => {
    expect(cardValue("spend", stats)).toBe("$1.23");
    expect(cardValue("tokens", stats)).toBe("1,234");
    expect(cardValue("attempts", stats)).toBe("10");
    expect(cardValue("first-token", stats)).toBe("1.5 s");
    expect(cardValue("cache", stats)).toBe("25%");
    expect(cardValue("sessions", stats)).toBe("3");
  });

  it("rounds the spend card to cents", () => {
    expect(cardValue("spend", { ...stats, total: { ...stats.total, cost: "7.966" } })).toBe("$7.97");
  });

  it("shows every card as missing when the period has no attempts", () => {
    const none: Stats = {
      ...stats,
      buckets: [],
      total: bucket("", {
        attempts: 0,
        requests: 0,
        cost: "0",
        input_tokens: observed(null),
        output_tokens: observed(null),
        cached_input: 0,
        cacheable_input: 0,
        first_token: { observed: 0, p50_ms: null, p95_ms: null },
      }),
    };
    for (const m of metrics) expect(cardValue(m.id, none)).toBe("—");
  });

  it("shows zero values once the period has attempts, but a percentile without data stays missing", () => {
    const quiet: Stats = {
      ...stats,
      buckets: [bucket("2026-11-09T10:00:00Z", { active_sessions: undefined })],
      total: bucket("", {
        cost: "0",
        input_tokens: observed(null),
        output_tokens: observed(null),
        cached_input: 0,
        cacheable_input: 0,
        first_token: { observed: 0, p50_ms: null, p95_ms: null },
      }),
    };
    expect(cardValue("spend", quiet)).toBe("$0.00");
    expect(cardValue("tokens", quiet)).toBe("0");
    expect(cardValue("cache", quiet)).toBe("0%");
    expect(cardValue("sessions", quiet)).toBe("0");
    expect(cardValue("first-token", quiet)).toBe("—");
    const uncached = { ...stats, total: { ...stats.total, cached_input: 0, cacheable_input: 500 } };
    expect(cardValue("cache", uncached)).toBe("0%");
  });

  it("splits attempts into three outcome series", () => {
    const s = cardSeries("attempts", stats);
    expect(s.map((x) => x.name)).toEqual(["Success", "Errors", "Cancelled"]);
    expect(s[0]?.values).toEqual([3, 3]);
  });

  it("keeps gaps in percentiles and plots the card value per bucket", () => {
    expect(cardSeries("first-token", stats).map((x) => x.values)).toEqual([
      [300, null],
      [900, null],
    ]);
    expect(cellValues("tokens", stats)).toEqual([120, 120]);
    expect(cellValues("cache", stats)).toEqual([40, 40]);
    expect(cellValues("spend", stats)).toEqual([0.5, 0.5]);
  });
});

describe("detail facts", () => {
  const value = (id: Parameters<typeof facts>[0], label: string) =>
    facts(id, stats).find((f) => f.label === label)?.value;

  it("lists the session costs and the average per request with the spend", () => {
    expect(value("spend", "Average per request")).toBe("$0.25");
    expect(value("spend", "Session p95")).toBe("$0.70");
    expect(value("spend", "Confirmed spend")).toBe("$1.23");
  });

  it("keeps the full amount in the title of a money fact", () => {
    const fact = (label: string) => facts("spend", stats).find((f) => f.label === label);
    expect(fact("Average per request")?.title).toBe("$0.246");
    expect(fact("Session p50")?.title).toBe("$0.5");
    expect(fact("Sessions")?.title).toBeUndefined();
  });

  it("counts extra attempts and the active-session peak", () => {
    expect(value("attempts", "Extra attempts")).toBe("6");
    expect(value("sessions", "Peak")).toBe("3");
  });

  it("moves the reporting count and the incomplete sessions into a faint note under the value", () => {
    const fact = (id: Parameters<typeof facts>[0], label: string) =>
      facts(id, stats).find((f) => f.label === label);
    expect(fact("tokens", "Reasoning")).toMatchObject({ value: "\u2014", note: "reported by 0 of 10" });
    expect(fact("spend", "Sessions")).toMatchObject({ value: "2", note: "0 incomplete" });
  });

  it("falls back to 24 hours for an unknown period", () => {
    expect(periodOf("7d")).toBe("7d");
    expect(periodOf("1y")).toBe("24h");
    expect(periodOf(undefined)).toBe("24h");
  });
});

describe("per-user series", () => {
  it("stacks additive metrics over the top users and the others", () => {
    const s = userSeries("spend", stats);
    expect(s.map((x) => x.name)).toEqual([...users, "Others"]);
    expect(s.every((x) => x.stack)).toBe(true);
  });

  it.each(["first-token", "cache"] as const)("draws %s as unstacked lines without the others", (id) => {
    const s = userSeries(id, stats);
    expect(s.map((x) => x.name)).toEqual(users);
    expect(s.some((x) => x.stack)).toBe(false);
  });

  it("has no series without group_by", () => {
    expect(userSeries("spend", { ...stats, series: undefined })).toEqual([]);
  });
});

describe("chart option", () => {
  const input = {
    starts: ["2026-11-09T10:00:00Z"],
    series: [{ name: "A", values: [1] }],
    kind: "bar" as const,
  };
  const colors = (name: string) => `color(${name})`;

  it("plots the cache cell as zero in buckets with attempts but no cacheable input", () => {
    const s: Stats = {
      ...stats,
      buckets: [
        bucket("a", { attempts: 0, cached_input: 0, cacheable_input: 0 }),
        bucket("b", { cached_input: 0, cacheable_input: 0 }),
        bucket("c"),
      ],
    };
    expect(cellValues("cache", s)).toEqual([null, 0, 40]);
    expect(cardSeries("cache", s)[0]?.values).toEqual([null, null, 40]);
  });

  it("colors series from the theme tokens", () => {
    const o = chartOption(
      {
        ...input,
        series: [
          { name: "Errors", values: [1], color: "--bad" },
          { name: "B", values: [2] },
        ],
      },
      colors,
    );
    const series = o.series as { itemStyle: { color: string } }[];
    expect(series.map((s) => s.itemStyle.color)).toEqual(["color(--bad)", "color(--chart-2)"]);
  });
});

describe("activity level", () => {
  it.each([
    [null, 10, 0],
    [0, 10, 0],
    [5, 0, 0],
    [1, 10, 1],
    [2.5, 10, 1],
    [2.6, 10, 2],
    [5, 10, 2],
    [7.5, 10, 3],
    [9, 10, 4],
    [10, 10, 4],
  ] as const)("maps %s of %s to level %s", (value, max, want) => {
    expect(level(value, max)).toBe(want);
  });
});
