import { describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createMemoryHistory } from "vue-router";
import App from "./App.vue";
import { createAppRouter } from "./router";
import type { Live } from "./live";
import * as fmt from "./format";
import type { Attempt, ClientKey } from "./api";

const tokens = { input: 120, output: 30, reasoning: null, cache_read: 100, cache_write: null };
const totals = { tokens, cost: "0.0004215", attempts: 2, pending: 1 };

const attempt: Attempt = {
  id: "a1",
  request_id: "r1",
  user_email: "dev@example.com",
  session_id: "s1",
  requested_model: "glm",
  upstream_model: "z-ai/glm-5.3",
  provider: "",
  inference_provider: "",
  generation_id: "gen-123",
  started_at: "2026-11-09T10:00:00Z",
  first_token_at: "2026-11-09T10:00:01Z",
  finished_at: "2026-11-09T10:00:03Z",
  outcome: "success",
  error_reason: "",
  tokens,
  cost: "0.0004215",
  cost_state: "confirmed",
};

const pending: Attempt = {
  ...attempt,
  id: "a2",
  cost: null,
  cost_state: "pending",
  outcome: "cancelled",
  error_reason: "client disconnected",
  generation_id: "",
};

const key: ClientKey = {
  id: 5,
  user_email: "dev@example.com",
  device_id: "laptop",
  device_name: "Laptop",
  created_at: "2026-11-09T10:00:00Z",
  expires_at: "2999-01-01T00:00:00Z",
  last_used_at: null,
  revoked_at: null,
};

describe("format", () => {
  it("never renders a missing value as zero", () => {
    expect(fmt.tokens(null)).toBe(fmt.missing);
    expect(fmt.tokens(0)).toBe("0");
    expect(fmt.tokens(12345)).toBe("12,345");
    expect(fmt.cost(null)).toBe("pending");
    expect(fmt.cost("0.01", "pending")).toBe("pending");
    expect(fmt.cost("0")).toBe("$0");
  });

  it("formats durations, outcomes and key states", () => {
    expect(fmt.duration("2026-11-09T10:00:00Z", "2026-11-09T10:00:00.250Z")).toBe("250 ms");
    expect(fmt.duration("2026-11-09T10:00:00Z", "2026-11-09T10:00:03.500Z")).toBe("3.5 s");
    expect(fmt.duration("2026-11-09T10:00:00Z", null)).toBe(fmt.missing);
    expect(fmt.outcome({ ...attempt, outcome: "" })).toBe("running");
    expect(fmt.time("2026-11-09T10:00:00Z")).toBe("2026-11-09 10:00:00 UTC");
    const now = Date.parse("2026-11-10T00:00:00Z");
    expect(fmt.keyState({ ...key, expires_at: "2026-11-11T00:00:00Z" }, now)).toBe("active");
    expect(fmt.keyState({ ...key, expires_at: "2026-11-09T00:00:00Z" }, now)).toBe("expired");
    expect(fmt.keyState({ ...key, revoked_at: "2026-11-09T00:00:00Z" }, now)).toBe("revoked");
  });
});

const incident = (id: number, resolved: string | null) => ({
  id,
  key: `k${id}`,
  severity: "critical",
  user_email: "",
  message: `incident ${id}`,
  opened_at: "2026-11-09T10:00:00Z",
  last_seen_at: "2026-11-09T10:05:00Z",
  last_notified_at: null,
  resolved_at: resolved,
  resolve_notified: false,
});

const keyOf = (id: number, email: string): ClientKey => ({ ...key, id, user_email: email });

// harness mounts the app at a path against a fake API; every request URL is recorded in calls.
async function harness(role: "admin" | "user", path: string, overrides: Record<string, unknown> = {}) {
  const calls: { url: string; method: string }[] = [];
  const handlers: Record<string, () => unknown> = {
    "/api/me": () => ({ email: "dev@example.com", role, credential: "session", status_fresh: true }),
    "/api/status": () => ({ inference: { state: "ok", reasons: [] }, models: [], version: "v", alerts: [] }),
    "/api/alerts": () => ({
      enabled: true,
      alerts: [incident(1, "2026-11-09T11:00:00Z"), incident(2, null), incident(3, null)],
    }),
    "/api/sessions": () => ({ sessions: [] }),
    "/api/users": () => ({
      users: [
        { email: "a@example.com", role: "user", tier: "basic", limits: {}, state: null, usage: [] },
        { email: "b@example.com", role: "user", tier: "basic", limits: {}, state: null, usage: [] },
      ],
    }),
    "/api/keys": () => ({ keys: [keyOf(1, "a@example.com"), keyOf(2, "b@example.com")] }),
    "/api/requests": () => ({ attempts: [attempt, pending, { ...pending, id: "a3", outcome: "" }] }),
    ...(overrides as Record<string, () => unknown>),
  };
  const fn = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({ url, method: init?.method ?? "GET" });
    const h = handlers[url.split("?")[0] ?? ""];
    if (!h) return new Response("{}", { status: 200 });
    const body = h();
    if (body instanceof Response) return body;
    // "?user=" filters keys the way the server does.
    const user = new URL(url, "http://x").searchParams.get("user");
    if (url.startsWith("/api/keys") && user && user !== "*") {
      const all = (body as { keys: ClientKey[] }).keys;
      return new Response(JSON.stringify({ keys: all.filter((k) => k.user_email === user) }));
    }
    return new Response(JSON.stringify(body));
  }) as typeof fetch;
  const live = { on: () => () => {}, onDropped: () => () => {}, close() {} } as unknown as Live;
  const router = createAppRouter(createMemoryHistory());
  await router.push(path);
  const wrapper = mount(App, { props: { fetchFn: fn, live }, global: { plugins: [router] } });
  await flushPromises();
  return { wrapper, calls, router };
}

describe("tables", () => {
  it("lets an administrator filter keys by user", async () => {
    const { wrapper, calls } = await harness("admin", "/keys");
    expect(calls.some((c) => c.url === "/api/keys")).toBe(true);
    await wrapper.find("select[aria-label='User']").setValue("*");
    await flushPromises();
    expect(calls.some((c) => c.url === "/api/keys?user=*")).toBe(true);
    expect(wrapper.findAll("table.keys tbody tr")).toHaveLength(2);
    await wrapper.find("select[aria-label='User']").setValue("b@example.com");
    await flushPromises();
    expect(calls.some((c) => c.url === "/api/keys?user=b%40example.com")).toBe(true);
    const rows = wrapper.findAll("table.keys tbody tr");
    expect(rows).toHaveLength(1);
    expect(rows[0]?.text()).toContain("Laptop");
  });

  it("gives a user their own keys without a filter", async () => {
    const { wrapper, calls } = await harness("user", "/keys");
    expect(wrapper.find("select[aria-label='User']").exists()).toBe(false);
    expect(calls.some((c) => c.url === "/api/keys")).toBe(true);
    expect(wrapper.text()).not.toContain("Revoke all for user");
  });

  it("revokes a key after confirmation and reloads the list", async () => {
    const proto = HTMLDialogElement.prototype as unknown as Record<string, unknown>;
    const saved = { showModal: proto.showModal, close: proto.close };
    proto.showModal = function (this: HTMLDialogElement) {
      this.setAttribute("open", "");
    };
    proto.close = function (this: HTMLDialogElement) {
      this.removeAttribute("open");
    };
    try {
      const { wrapper, calls } = await harness("user", "/keys");
      await wrapper
        .findAll("table.keys button")
        .find((b) => b.text() === "Revoke")!
        .trigger("click");
      await wrapper.find("dialog button.danger").trigger("click");
      await flushPromises();
      expect(calls.some((c) => c.method === "DELETE" && c.url === "/api/keys/1")).toBe(true);
    } finally {
      Object.assign(proto, saved);
    }
  });

  it("lists open alerts before resolved ones", async () => {
    const { wrapper } = await harness("admin", "/alerts?status=all");
    const rows = wrapper.findAll("table tbody tr").map((r) => r.text());
    expect(rows.map((r) => /incident (\d)/.exec(r)![1])).toEqual(["2", "3", "1"]);
  });

  it("lists users for an administrator", async () => {
    const { wrapper } = await harness("admin", "/users");
    expect(wrapper.findAll("table tbody tr")).toHaveLength(2);
  });

  it("has no Refresh button", async () => {
    const { wrapper } = await harness("admin", "/sessions");
    expect(wrapper.text()).not.toContain("Refresh");
  });
});

describe("shell actions", () => {
  it("returns to the signed-out state after sign-out", async () => {
    const { wrapper } = await harness("user", "/sessions");
    await wrapper
      .findAll("button")
      .find((b) => b.text() === "Sign out")!
      .trigger("click");
    await flushPromises();
    expect(wrapper.find("a[href='/auth/gitlab/login']").exists()).toBe(true);
    expect(wrapper.find("nav").exists()).toBe(false);
  });

  it("renders a server error as an alert", async () => {
    const { wrapper } = await harness("user", "/sessions", {
      "/api/status": () =>
        new Response(JSON.stringify({ error: { message: "database unavailable" } }), { status: 503 }),
    });
    expect(wrapper.find("[role='alert']").text()).toContain("database unavailable");
  });
});
