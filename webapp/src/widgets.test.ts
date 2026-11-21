import { describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { defineComponent, h, ref, type Component } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import { Client, clientKey, identityKey, type Identity } from "./api";
import { Live, liveKey } from "./live";
import { routes } from "./router";
import BudgetWidget from "./widgets/BudgetWidget.vue";
import ModelsWidget from "./widgets/ModelsWidget.vue";
import StatusWidget from "./widgets/StatusWidget.vue";
import StatusIndicator from "./components/StatusIndicator.vue";
import TopBar from "./components/TopBar.vue";
import UsersView from "./views/UsersView.vue";
import TiersWidget from "./widgets/TiersWidget.vue";
import UsageWidget from "./widgets/UsageWidget.vue";
import MetricDetailView from "./views/MetricDetailView.vue";
import OverviewView from "./views/OverviewView.vue";
import UsageView from "./views/UsageView.vue";

// ECharts needs a canvas; the stub keeps the option so tests can read what a chart was given.
vi.mock("vue-echarts", () => ({
  default: defineComponent({
    props: { option: { type: Object, default: () => ({}) } },
    setup: (props) => () => h("div", { "data-chart": JSON.stringify(props.option) }),
  }),
}));

const bucket = {
  start: "2026-11-09T10:00:00Z",
  attempts: 2,
  requests: 2,
  success: 2,
  errors: 0,
  cancelled: 0,
  running: 0,
  pending: 0,
  confirmed: 2,
  cost: "0.4",
  input_tokens: { sum: 10, reported: 2 },
  output_tokens: { sum: 5, reported: 2 },
  reasoning_tokens: { sum: null, reported: 0 },
  cache_read_tokens: { sum: null, reported: 0 },
  cache_write_tokens: { sum: null, reported: 0 },
  cached_input: 0,
  cacheable_input: 0,
  first_token: { observed: 2, p50_ms: 200, p95_ms: 400 },
  duration: { observed: 2, p50_ms: 900, p95_ms: 1200 },
  active_sessions: 1,
};

const stats = {
  bucket_seconds: 3600,
  stats: {
    buckets: [bucket],
    total: bucket,
    by_user: [{ key: "dev@example.com", attempts: 2, pending: 0, cost: "0.4" }],
    by_model: [{ key: "glm", attempts: 2, pending: 0, cost: "0.4" }],
    by_provider: [],
    sessions: { sessions: 1, incomplete: 0, average: "0.4", p50: "0.4", p95: "0.4" },
    oldest_pending: null,
  },
};

const windowOf = (scope: string, window: string, percent: number, spent = "2", unsettled = 0) => ({
  scope,
  window,
  spent,
  limit: "10",
  percent,
  resets_at: "2026-11-09T15:00:00Z",
  unsettled,
});

const health = {
  model: "glm",
  attempts: 7,
  errors: 2,
  first_token_p95_ms: 1200,
  last_success_at: "2026-11-09T10:00:00Z",
  last_error_at: "2026-11-09T09:00:00Z",
  last_error_reason: "upstream timeout",
};

const limits = { "5_hours": "5", day: "10", week: "40", month: "100" };

function answer(url: string, paused: boolean): unknown {
  if (url.startsWith("/api/stats")) return stats;
  if (url.startsWith("/api/budget"))
    return {
      warn_at_percent: [80, 95],
      windows: [
        windowOf("user", "5_hours", 20),
        windowOf("user", "day", 85, "2", 2),
        windowOf("user", "week", 10, "4.1851234"),
        windowOf("user", "month", 5),
        windowOf("global", "day", 40),
        windowOf("global", "month", 3),
      ],
    };
  if (url.startsWith("/api/status"))
    return {
      inference: {
        state: paused ? "paused" : "ok",
        reasons: paused ? [{ code: "own_budget_exhausted" }] : [],
      },
      models: [],
      version: "v",
      alerts: [],
    };
  if (url.startsWith("/api/users"))
    return {
      users: [
        {
          email: "dev@example.com",
          role: "user",
          tier: "standard",
          state: null,
          limits,
          usage: [{ window: "day", spent: "7.9661234", percent: 79, unsettled: 1 }],
        },
        { email: "b@x", role: "user", tier: "standard", state: null, limits, usage: [] },
      ],
    };
  if (url.startsWith("/api/usage"))
    return {
      window: "day",
      start: "2026-11-09T00:00:00Z",
      end: "2026-11-10T00:00:00Z",
      total: { tokens: {}, cost: "0.0659218", attempts: 3, pending: 0 },
      groups: [{ key: "glm", tokens: {}, cost: "0.0659218", attempts: 3, pending: 0 }],
    };
  if (url.startsWith("/api/models"))
    return {
      models: [
        {
          name: "glm",
          input_token_limit: 128000,
          max_output_tokens: 8000,
          reasoning: false,
          price_input_per_mtok: "0.3",
          price_output_per_mtok: "1.2",
        },
        {
          name: "kimi",
          input_token_limit: 200000,
          max_output_tokens: 16000,
          reasoning: true,
          price_input_per_mtok: null,
          price_output_per_mtok: null,
        },
      ],
      default: "glm",
    };
  if (url.startsWith("/api/system"))
    return {
      checks: { postgres: "ok", openrouter: "unreachable" },
      version: "1.2.3",
      models: [
        {
          name: "glm",
          upstream_id: "z-ai/glm-4.6",
          input_token_limit: 1,
          max_output_tokens: 1,
          reasoning_effort: "high",
          price_input_per_mtok: "0.075",
          price_output_per_mtok: "15",
          status: "unknown_upstream",
          health,
        },
        {
          name: "kimi",
          input_token_limit: 200000,
          max_output_tokens: 16000,
          reasoning_effort: "",
          price_input_per_mtok: null,
          price_output_per_mtok: null,
          status: "ok",
          health: null,
        },
      ],
      configuration: { revision: "sha256:abcdef", loaded_at: "2026-11-09T10:00:00Z", error: "bad tier" },
      backup: { status: "attention", problem: "S3 unreachable", last_success_at: "2026-11-08T03:00:00Z" },
      tiers: [
        { name: "standard", default: true, budget: limits, models: [{ name: "glm", default: true }] },
        {
          name: "pro",
          default: false,
          budget: { "5_hours": "20", day: "50", week: "200", month: "500" },
          models: [
            { name: "glm", default: false },
            { name: "kimi", default: true },
          ],
        },
      ],
    };
  return {};
}

// failing answers a URL prefix with an HTTP error, answers replaces a prefix's fixture, and pending prefixes
// never answer.
interface FetchOpts {
  paused?: boolean;
  failing?: Record<string, number>;
  answers?: Record<string, unknown>;
  pending?: string[];
}

function fakeFetch({ paused = false, failing = {}, answers = {}, pending = [] }: FetchOpts = {}) {
  const calls: string[] = [];
  const fn = (async (input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (pending.some((p) => url.startsWith(p))) return new Promise<Response>(() => {});
    for (const [prefix, code] of Object.entries(failing))
      if (url.startsWith(prefix))
        return new Response(JSON.stringify({ error: { message: `failed ${code}` } }), { status: code });
    const own = Object.entries(answers).find(([prefix]) => url.startsWith(prefix));
    return new Response(JSON.stringify(own ? own[1] : answer(url, paused)), { status: 200 });
  }) as typeof fetch;
  return { fn, calls };
}

function stubLive() {
  const handlers = new Map<string, (() => void)[]>();
  const live = {
    on(topic: string, fn: () => void) {
      handlers.set(topic, [...(handlers.get(topic) ?? []), fn]);
      return () => {};
    },
    close() {},
  } as unknown as Live;
  return {
    live,
    topics: () => [...handlers.keys()],
    fire: (t: string) => handlers.get(t)?.forEach((f) => f()),
  };
}

async function render(
  component: Component,
  props: Record<string, unknown>,
  opts: { path?: string; role?: "admin" | "user" } & FetchOpts = {},
) {
  const f = fakeFetch(opts);
  const router = createRouter({ history: createMemoryHistory(), routes });
  await router.push(opts.path ?? "/usage");
  const s = stubLive();
  const me: Identity = {
    email: "dev@example.com",
    role: opts.role ?? "user",
    credential: "session",
    status_fresh: true,
  };
  const wrapper = mount(component, {
    props,
    global: {
      plugins: [router],
      provide: {
        [clientKey as symbol]: new Client(f.fn),
        [liveKey as symbol]: s.live,
        [identityKey as symbol]: ref(me),
      },
    },
  });
  await flushPromises();
  return { wrapper, router, ...f, ...s };
}

const query = (url: string) => new URL(url, "http://x").searchParams;

// titles lists the card titles directly inside the row a selector picks.
const titles = (w: ReturnType<typeof mount>, sel: string) =>
  w.findAll(`${sel} > section h2`).map((h) => h.text());

describe("budget widget", () => {
  it("renders one bar per user window with marks at the thresholds", async () => {
    const { wrapper, topics } = await render(BudgetWidget, { scope: "user" });
    const bars = wrapper.findAll("[data-window]");
    expect(bars).toHaveLength(4);
    expect(bars[0]?.text()).toContain("5 hours");
    expect(bars[0]?.text()).toContain("$8.00 left");
    expect(bars[2]?.text()).toContain("$5.81 left");
    expect(bars[2]?.text()).toContain("$4.19 of $10.00");
    for (const bar of bars)
      expect(bar.findAll("[data-mark]").map((m) => m.attributes("style"))).toEqual([
        "left: 80%;",
        "left: 95%;",
      ]);
    expect(topics()).toContain("budget");
  });

  it("counts down to each reset with the exact UTC time in the tooltip", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-11-09T12:30:00Z"));
    try {
      const { wrapper } = await render(BudgetWidget, { scope: "user" });
      const reset = wrapper.find("[data-resets]");
      expect(reset.text()).toBe("resets in 2h 30m");
      expect(reset.attributes("title")).toBe("2026-11-09 15:00:00 UTC");
    } finally {
      vi.useRealTimers();
    }
  });

  it("refetches on status events and when the earliest window resets", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    vi.setSystemTime(new Date("2026-11-09T14:59:00Z"));
    try {
      const { wrapper, calls, topics, fire } = await render(BudgetWidget, { scope: "user" });
      const budgetCalls = () => calls.filter((u) => u.startsWith("/api/budget")).length;
      expect(topics()).toEqual(expect.arrayContaining(["budget", "status"]));
      fire("status");
      await flushPromises();
      expect(budgetCalls()).toBe(2);
      vi.advanceTimersByTime(59_000);
      await flushPromises();
      expect(budgetCalls()).toBe(2);
      vi.advanceTimersByTime(2_000);
      await flushPromises();
      expect(budgetCalls()).toBe(3);
      wrapper.unmount();
      vi.advanceTimersByTime(24 * 3600_000);
      await flushPromises();
      expect(budgetCalls()).toBe(3);
    } finally {
      vi.useRealTimers();
    }
  });

  it("marks only the windows with unsettled requests and says how many", async () => {
    const { wrapper } = await render(BudgetWidget, { scope: "user" });
    const marked = wrapper.findAll("[data-window]").filter((b) => b.find("[data-unsettled]").exists());
    expect(marked.map((b) => b.attributes("data-window"))).toEqual(["day"]);
    const mark = marked[0]!.find("[data-unsettled]");
    expect(mark.attributes("title")).toBe("2 finished request(s) with an unknown cost; not counted yet");
    expect(mark.attributes("aria-label")).toBe(mark.attributes("title"));
  });

  it("shows only the global windows for the system", async () => {
    const { wrapper } = await render(BudgetWidget, { scope: "global" });
    expect(wrapper.findAll("[data-window]").map((b) => b.attributes("data-window"))).toEqual([
      "day",
      "month",
    ]);
  });
});

describe("usage widget", () => {
  it("renders six cards linking to the caller's detail views", async () => {
    const { wrapper, calls, topics } = await render(UsageWidget, { scope: "me" });
    const links = wrapper.findAll("a[data-card]").map((a) => a.attributes("href"));
    expect(links).toEqual([
      "/usage/spend?period=24h",
      "/usage/tokens?period=24h",
      "/usage/attempts?period=24h",
      "/usage/first-token?period=24h",
      "/usage/cache?period=24h",
      "/usage/sessions?period=24h",
    ]);
    expect(wrapper.text()).toContain("$0.40");
    const statsCall = calls.find((u) => u.startsWith("/api/stats")) ?? "";
    expect(query(statsCall).get("user")).toBeNull();
    expect(topics()).toContain("usage");
  });

  it("opens the system detail views for everyone's usage and keeps the period", async () => {
    const { wrapper, calls } = await render(
      UsageWidget,
      { scope: "system" },
      { path: "/overview?period=7d", role: "admin" },
    );
    expect(wrapper.find("a[data-card]").attributes("href")).toBe("/overview/spend?period=7d");
    const statsCall = calls.find((u) => u.startsWith("/api/stats")) ?? "";
    expect(query(statsCall).get("user")).toBe("*");
  });

  it("makes each card one link holding its title, period, value and activity grid", async () => {
    const { wrapper } = await render(UsageWidget, { scope: "me" });
    const cards = wrapper.findAll("[data-card]");
    const card = cards[0];
    expect(card?.element.tagName).toBe("A");
    expect(card?.classes()).toContain("clickable-card");
    // Title left, the faint short period right, the value on its own line below.
    expect(card?.find(".card-head .title").text()).toBe("Confirmed spend");
    expect(card?.find(".card-head [data-period]").text()).toBe("24h");
    expect(card?.find(".card-head [data-period]").classes()).toContain("muted");
    expect(card?.find(".value").text()).toBe("$0.40");
    expect(card?.find(".value").classes()).toContain("mono");
    expect(card?.find("[data-chart]").exists()).toBe(false);
    expect(card?.findAll("[data-cell]")).toHaveLength(24);
    expect(card?.findAll("a")).toHaveLength(0);
    expect(cards[2]?.text()).toContain("LLM requests");
    expect(wrapper.text()).not.toContain("Attempts by outcome");
  });

  it.each([
    ["7d", "7d", "[data-cell]", 7 * 24],
    ["30d", "30d", "[data-cell]:not([data-blank])", 30],
  ])("states the %s period on every card and lays out its grid", async (period, label, cell, count) => {
    const { wrapper } = await render(UsageWidget, { scope: "me" }, { path: `/usage?period=${period}` });
    for (const card of wrapper.findAll("[data-card]")) expect(card.find("[data-period]").text()).toBe(label);
    expect(wrapper.findAll("[data-card]")[0]!.findAll(cell)).toHaveLength(count);
  });

  it("shows the cached share in the grid over 30 days", async () => {
    // Day buckets start at midnight UTC.
    const day = { ...bucket, start: "2026-11-09T00:00:00Z" };
    const { wrapper } = await render(
      UsageWidget,
      { scope: "me" },
      {
        path: "/usage?period=30d",
        answers: { "/api/stats": { ...stats, stats: { ...stats.stats, buckets: [day] } } },
      },
    );
    const card = wrapper.findAll("[data-card]")[4];
    expect(card?.text()).toContain("Cached share of input");
    expect(card?.text()).toContain("0%");
    expect(card?.find("[title$=': 0%']").exists()).toBe(true);
  });

  it("asks for one user's usage and keeps the user in the card links", async () => {
    const { wrapper, calls } = await render(
      UsageWidget,
      { scope: "me", user: "b@x" },
      { path: "/usage?user=b@x", role: "admin" },
    );
    expect(query(calls.find((u) => u.startsWith("/api/stats")) ?? "").get("user")).toBe("b@x");
    expect(wrapper.find("a[data-card]").attributes("href")).toBe("/usage/spend?period=24h&user=b@x");
  });

  it("refetches on a usage event", async () => {
    const { calls, fire } = await render(UsageWidget, { scope: "me" });
    const before = calls.filter((u) => u.startsWith("/api/stats")).length;
    fire("usage");
    await flushPromises();
    expect(calls.filter((u) => u.startsWith("/api/stats")).length).toBe(before + 1);
  });
});

describe("status widget", () => {
  it("tells a user why requests are paused, without system checks", async () => {
    const { wrapper, calls, topics } = await render(StatusWidget, { admin: false }, { paused: true });
    expect(wrapper.text()).toContain("Your budget is exhausted");
    expect(wrapper.find("[data-checks]").exists()).toBe(false);
    expect(calls.some((u) => u.startsWith("/api/system"))).toBe(false);
    expect(topics()).toContain("status");
  });

  it("shows an administrator the checks and health without a System details block", async () => {
    const { wrapper } = await render(StatusWidget, { admin: true }, { role: "admin" });
    const checks = wrapper.find("[data-checks]");
    expect(checks.text()).toContain("postgres");
    expect(checks.text()).toContain("unreachable");
    expect(wrapper.find("details").exists()).toBe(false);
    const text = wrapper.text();
    expect(text).not.toContain("System details");
    for (const s of ["S3 unreachable", "bad tier", "sha256:abcdef", "backup"]) expect(text).toContain(s);
    for (const s of ["upstream timeout", "z-ai/glm-4.6", "Tier budgets", "oldest unknown cost"])
      expect(text).not.toContain(s);
  });
});

describe("status widget pills", () => {
  it("has no Requests run pill while requests run and says when they are paused", async () => {
    const running = await render(StatusWidget, { admin: true }, { role: "admin" });
    expect(running.wrapper.text()).not.toContain("Requests run");
    const paused = await render(StatusWidget, { admin: true }, { role: "admin", paused: true });
    expect(paused.wrapper.text()).toContain("Requests paused");
  });
});

describe("status indicator", () => {
  const healthy = { "/api/system": { checks: { postgres: "ok", openrouter: "ok" }, version: "1" } };
  const pill = (w: ReturnType<typeof mount>) => w.find("button[data-status-indicator]");
  const open = async (w: ReturnType<typeof mount>) => {
    await pill(w).trigger("click");
    return w.find("[data-status-popover]");
  };

  it("shows the state as an icon only, named for screen readers", async () => {
    const { wrapper } = await render(StatusIndicator, { admin: true }, { role: "admin", answers: healthy });
    expect(pill(wrapper).text()).toBe("");
    expect(pill(wrapper).find("svg").exists()).toBe(true);
    expect(pill(wrapper).attributes("aria-label")).toBe("Status: Running");
  });

  it("says Running when inference runs and every check is ok", async () => {
    const { wrapper, topics } = await render(
      StatusIndicator,
      { admin: true },
      { role: "admin", answers: healthy },
    );
    expect(pill(wrapper).attributes("title")).toBe("Running");
    expect(pill(wrapper).classes()).toContain("ok");
    expect(topics()).toContain("status");
  });

  it("says Degraded when inference runs but a check is not ok", async () => {
    const { wrapper } = await render(StatusIndicator, { admin: true }, { role: "admin" });
    expect(pill(wrapper).attributes("title")).toBe("Degraded");
    expect(pill(wrapper).classes()).toContain("warn");
  });

  it("says Paused and lists the pause reasons in the popover", async () => {
    const { wrapper } = await render(StatusIndicator, { admin: false }, { paused: true });
    expect(pill(wrapper).attributes("title")).toBe("Paused");
    expect(pill(wrapper).classes()).toContain("bad");
    expect(pill(wrapper).attributes("aria-expanded")).toBe("false");
    const popover = await open(wrapper);
    expect(pill(wrapper).attributes("aria-expanded")).toBe("true");
    expect(popover.text()).toContain("Your budget is exhausted");
  });

  it("says Unknown before the first answer", async () => {
    const { wrapper } = await render(StatusIndicator, { admin: false }, { pending: ["/api/status"] });
    expect(pill(wrapper).attributes("title")).toBe("Unknown");
    expect(pill(wrapper).classes()).toContain("muted");
  });

  it("says Unknown when the status fails", async () => {
    const { wrapper } = await render(StatusIndicator, { admin: false }, { failing: { "/api/status": 503 } });
    expect(pill(wrapper).attributes("title")).toBe("Unknown");
    expect(pill(wrapper).classes()).toContain("muted");
    expect((await open(wrapper)).text()).toContain("failed 503");
  });

  it("lists the checks for an administrator", async () => {
    const { wrapper } = await render(StatusIndicator, { admin: true }, { role: "admin" });
    const checks = (await open(wrapper)).find("[data-checks]");
    expect(checks.text()).toContain("postgres");
    expect(checks.text()).toContain("unreachable");
  });

  it("shows a user no checks and never asks for them", async () => {
    const { wrapper, calls } = await render(StatusIndicator, { admin: false });
    expect(pill(wrapper).attributes("title")).toBe("Running");
    expect((await open(wrapper)).find("[data-checks]").exists()).toBe(false);
    expect(calls.some((u) => u.startsWith("/api/system"))).toBe(false);
  });

  it("closes the popover on Escape", async () => {
    const { wrapper } = await render(StatusIndicator, { admin: false });
    await open(wrapper);
    await wrapper.find("[data-status-indicator]").trigger("keydown", { key: "Escape" });
    expect(wrapper.find("[data-status-popover]").exists()).toBe(false);
  });
});

describe("status widget during an outage", () => {
  it("keeps the pause reasons when the system details fail", async () => {
    const { wrapper } = await render(
      StatusWidget,
      { admin: true },
      { role: "admin", paused: true, failing: { "/api/system": 503 } },
    );
    expect(wrapper.text()).toContain("Your budget is exhausted");
    expect(wrapper.find(".error").text()).toContain("failed 503");
  });

  it("keeps the last successful backup", async () => {
    const { wrapper } = await render(StatusWidget, { admin: true }, { role: "admin" });
    expect(wrapper.text()).toContain("2026-11-08 03:00:00 UTC");
  });
});

describe("users view", () => {
  it("refetches on users, budget and keys events and rounds the spend", async () => {
    const { wrapper, calls, topics, fire } = await render(UsersView, {}, { role: "admin", path: "/users" });
    expect(topics()).toEqual(expect.arrayContaining(["users", "budget", "keys"]));
    expect(wrapper.text()).toContain("79%");
    expect(wrapper.text()).toContain("$7.97");
    const before = calls.filter((u) => u.startsWith("/api/users")).length;
    fire("budget");
    fire("keys");
    await flushPromises();
    expect(calls.filter((u) => u.startsWith("/api/users")).length).toBe(before + 2);
  });

  it("marks a window cell that has unsettled requests", async () => {
    const { wrapper } = await render(UsersView, {}, { role: "admin", path: "/users" });
    const marks = wrapper.findAll("td.spend [data-unsettled]");
    expect(marks).toHaveLength(1);
    expect(marks[0]?.attributes("title")).toBe("1 finished request(s) with an unknown cost; not counted yet");
  });
});

describe("top bar", () => {
  it("shows the alert count as a badge without turning the tab into a pill", async () => {
    const { wrapper } = await render(TopBar, {
      me: { email: "a@x", role: "admin", credential: "session", status_fresh: true },
      tabs: [{ path: "/alerts", label: "Alerts", count: 2 }],
    });
    const tab = wrapper.find("nav a");
    expect(tab.classes()).not.toContain("pill");
    expect(tab.find(".count").text()).toBe("2");
  });
});

describe("models widget", () => {
  it("wraps the model table so it scrolls inside its card", async () => {
    const { wrapper } = await render(ModelsWidget, { admin: false });
    expect(wrapper.find(".scroll table").exists()).toBe(true);
  });

  it("lists the caller's models with the default", async () => {
    const { wrapper, topics } = await render(ModelsWidget, { admin: false });
    expect(wrapper.text()).toContain("kimi");
    expect(wrapper.find("[data-default]").text()).toContain("glm");
    expect(topics()).toContain("status");
  });

  it("shows a compact context instead of the input and output limits", async () => {
    const { wrapper } = await render(ModelsWidget, { admin: false });
    const heads = wrapper.findAll("th").map((t) => t.text());
    expect(heads).toContain("Context");
    expect(heads).not.toContain("Input limit");
    expect(heads).not.toContain("Output limit");
    expect(wrapper.find("[data-default]").text()).toContain("128K");
    expect(wrapper.text()).not.toContain("16,000");
  });

  // headRows lists each header row's cells.
  const headRows = (w: { findAll: (s: string) => { findAll: (s: string) => { text: () => string }[] }[] }) =>
    w.findAll("thead tr").map((r) => r.findAll("th").map((t) => t.text()));

  it("notes the models, their prices and the caller's requests and spend today", async () => {
    const { wrapper, calls, topics } = await render(ModelsWidget, { admin: false });
    expect(wrapper.find(".note-under-title").text()).toBe("Models you may use with Wrench.");
    expect(headRows(wrapper)).toEqual([
      ["Model", "Context", "Price in", "Price out", "Requests today", "Spend today"],
    ]);
    const cells = (i: number) =>
      wrapper
        .findAll("tbody tr")
        [i]!.findAll("td")
        .map((td) => td.text());
    expect(cells(0).slice(2)).toEqual(["$0.30/1M tokens", "$1.20/1M tokens", "3", "$0.07"]);
    expect(wrapper.find("tbody td small").text()).toBe("/1M tokens");
    expect(cells(1).slice(2)).toEqual(["\u2014", "\u2014", "0", "$0.00"]);
    const usageCall = calls.find((u) => u.startsWith("/api/usage")) ?? "";
    expect(query(usageCall).get("window")).toBe("day");
    expect(query(usageCall).get("group")).toBe("model");
    expect(query(usageCall).get("user")).toBeNull();
    expect(topics()).toContain("usage");
  });

  it("draws a spend bar sized against the day's largest model spend", async () => {
    const { wrapper } = await render(ModelsWidget, { admin: false });
    const bars = wrapper.findAll("tbody tr").map((r) => r.find("[data-spend-bar]"));
    expect(bars[0]!.attributes("style")).toContain("width: 100%");
    expect(bars[1]!.attributes("style")).toContain("width: 0%");
  });

  it("lists every configured model with context and prices for an administrator", async () => {
    const { wrapper, calls } = await render(ModelsWidget, { admin: true }, { role: "admin" });
    expect(calls.some((u) => u.startsWith("/api/models"))).toBe(false);
    expect(calls.some((u) => u.startsWith("/api/usage"))).toBe(false);
    expect(headRows(wrapper)).toEqual([["Model", "Context", "Price in", "Price out"]]);
    const rows = wrapper.findAll("tbody tr");
    expect(rows.map((r) => r.find("td").text())).toEqual([
      expect.stringContaining("glm"),
      expect.stringContaining("kimi"),
    ]);
    expect(wrapper.find(".pill.status").exists()).toBe(false);
    expect(wrapper.text()).not.toContain("1.2 s");
    const title = rows[0]!.attributes("title") ?? "";
    for (const s of ["z-ai/glm-4.6", "Attempts: 7", "Last success:", "Last error:", "upstream timeout"])
      expect(title).toContain(s);
    expect(
      rows[0]!
        .findAll("td")
        .map((td) => td.text())
        .slice(2),
    ).toEqual(["$0.075/1M tokens", "$15.00/1M tokens"]);
    expect(rows[1]?.text()).toContain("200K");
  });
});

describe("tiers widget", () => {
  it("lists each tier with its models, default model, default mark and budget", async () => {
    const { wrapper, topics } = await render(TiersWidget, {}, { role: "admin" });
    const rows = wrapper.findAll("tbody tr");
    expect(rows).toHaveLength(2);
    expect(rows[0]?.find("[data-default-tier]").exists()).toBe(true);
    expect(rows[1]?.find("[data-default-tier]").exists()).toBe(false);
    const pro = rows[1]?.text() ?? "";
    for (const s of ["pro", "glm, kimi", "$20.00", "$50.00", "$200.00", "$500.00"]) expect(pro).toContain(s);
    expect(rows[1]?.find("[data-default-model]").text()).toBe("kimi");
    expect(rows[0]?.find("[data-default-model]").text()).toBe("glm");
    expect(topics()).toContain("status");
  });
});

describe("overview and usage views", () => {
  it("puts Status, the shared budget, Models and Tiers in a two-by-two grid on Overview", async () => {
    const { wrapper } = await render(OverviewView, {}, { role: "admin", path: "/overview" });
    expect(titles(wrapper, ".grid-2x2")).toEqual(["Status", "Shared budget", "Models", "Tiers"]);
    expect(wrapper.find(".row-3").exists()).toBe(false);
  });

  it("puts Models (two thirds) and the caller's budget (one third) in one row on Usage, without Status or a user select", async () => {
    const { wrapper, calls } = await render(UsageView, {}, { path: "/usage" });
    expect(titles(wrapper, ".row-2-1")).toEqual(["Models", "Your budget"]);
    expect(wrapper.find(".row-2").exists()).toBe(false);
    expect(wrapper.find(".row-3").exists()).toBe(false);
    expect(wrapper.find("select[aria-label='User']").exists()).toBe(false);
    expect(calls.some((u) => u.startsWith("/api/users"))).toBe(false);
  });

  it("offers an administrator the user select defaulting to themselves", async () => {
    const { wrapper } = await render(UsageView, {}, { role: "admin", path: "/usage" });
    const select = wrapper.find("select[aria-label='User']");
    expect((select.element as HTMLSelectElement).value).toBe("");
    expect(select.findAll("option").map((o) => o.text())).toEqual([
      "Me (dev@example.com)",
      "All users",
      "b@x",
    ]);
  });

  it("shows one user's budget and usage from the query", async () => {
    const { calls } = await render(UsageView, {}, { role: "admin", path: "/usage?user=b@x" });
    expect(query(calls.find((u) => u.startsWith("/api/stats")) ?? "").get("user")).toBe("b@x");
    expect(query(calls.find((u) => u.startsWith("/api/budget")) ?? "").get("user")).toBe("b@x");
  });

  it("shows the system cards and the global budget for all users", async () => {
    const { wrapper, calls } = await render(UsageView, {}, { role: "admin", path: "/usage?user=*" });
    expect(query(calls.find((u) => u.startsWith("/api/stats")) ?? "").get("user")).toBe("*");
    expect(query(calls.find((u) => u.startsWith("/api/budget")) ?? "").get("user")).toBeNull();
    expect(titles(wrapper, ".row-2-1")).toEqual(["Models", "Shared budget"]);
    expect(wrapper.text()).toContain("System usage");
  });

  it("writes the selected user into the query", async () => {
    const { wrapper, router } = await render(UsageView, {}, { role: "admin", path: "/usage?period=7d" });
    await wrapper.find("select[aria-label='User']").setValue("b@x");
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ period: "7d", user: "b@x" });
    await wrapper.find("select[aria-label='User']").setValue("");
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ period: "7d" });
  });
});

describe("metric detail", () => {
  it("shows the facts as equal cards: title, accent value and a faint note", async () => {
    const { wrapper } = await render(MetricDetailView, {}, { path: "/usage/tokens?period=24h" });
    const cards = wrapper.findAll(".facts .fact-card");
    expect(cards.length).toBeGreaterThan(0);
    const input = cards.find((c) => c.find(".fact-title").text() === "Input")!;
    expect(input.find(".fact-value").classes()).toEqual(expect.arrayContaining(["mono", "accent"]));
    expect(input.find(".fact-note").text()).toMatch(/^reported by \d+ of \d+$/);
    expect(input.find(".fact-note").classes()).toContain("muted");
  });

  it("reads the period and model from the query and shows the cost tables", async () => {
    const { wrapper, calls } = await render(
      MetricDetailView,
      {},
      { path: "/usage/spend?period=30d&model=glm" },
    );
    const q = query(calls.find((u) => u.startsWith("/api/stats")) ?? "");
    expect(q.get("bucket")).toBe("day");
    expect(q.get("model")).toBe("glm");
    expect(q.get("user")).toBeNull();
    expect(wrapper.text()).toContain("By model");
    expect(titles(wrapper, ".row-3")).toEqual(["By user", "By model", "By inference provider"]);
    expect(wrapper.find("[data-by-user]").exists()).toBe(false);
  });

  it("shows money in the cost tables in cents with the full amount in the title", async () => {
    const { wrapper } = await render(MetricDetailView, {}, { path: "/usage/spend" });
    const cell = wrapper.find("tbody td:nth-child(3)");
    expect(cell.text()).toBe("$0.40");
    expect(cell.attributes("title")).toBe("$0.4");
  });

  it("titles the attempts detail LLM requests", async () => {
    const { wrapper } = await render(MetricDetailView, {}, { path: "/usage/attempts" });
    expect(wrapper.find("h2").text()).toBe("LLM requests");
  });

  it("asks for everyone's per-user series when an administrator toggles by user", async () => {
    const { wrapper, calls } = await render(
      MetricDetailView,
      {},
      { path: "/overview/tokens", role: "admin" },
    );
    expect(query(calls.filter((u) => u.startsWith("/api/stats")).at(-1) ?? "").get("user")).toBe("*");
    await wrapper.find("[data-by-user] input").setValue(true);
    await flushPromises();
    const q = query(calls.filter((u) => u.startsWith("/api/stats")).at(-1) ?? "");
    expect(q.get("group_by")).toBe("user");
    expect(q.get("user")).toBe("*");
  });

  it("keeps one user from the query and offers no by-user split for them", async () => {
    const { wrapper, calls } = await render(
      MetricDetailView,
      {},
      { path: "/usage/tokens?user=b@x", role: "admin" },
    );
    expect(query(calls.filter((u) => u.startsWith("/api/stats")).at(-1) ?? "").get("user")).toBe("b@x");
    expect(wrapper.find("[data-by-user]").exists()).toBe(false);
    expect((wrapper.find("select[aria-label='User']").element as HTMLSelectElement).value).toBe("b@x");
    expect(wrapper.find("a").attributes("href")).toBe("/usage?user=b@x");
  });

  it("offers the by-user split on the usage detail for all users", async () => {
    const { wrapper, calls } = await render(
      MetricDetailView,
      {},
      { path: "/usage/tokens?user=*", role: "admin" },
    );
    expect(query(calls.filter((u) => u.startsWith("/api/stats")).at(-1) ?? "").get("user")).toBe("*");
    expect(wrapper.find("[data-by-user]").exists()).toBe(true);
  });
});
