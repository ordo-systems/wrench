import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { defineComponent } from "vue";
import { createMemoryHistory, createRouter, useRoute, type RouteRecordRaw } from "vue-router";
import App from "./App.vue";
import AlertBanner from "./components/AlertBanner.vue";
import { Live } from "./live";
import { createAppRouter, routes } from "./router";
import { initTheme, toggleTheme } from "./theme";

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
});

function fakeFetch(role: "admin" | "user", statusAlerts: unknown[] = []) {
  const calls: string[] = [];
  let alerts = [incident(1, null), incident(2, null), incident(3, "2026-11-09T11:00:00Z")];
  // failing maps a URL prefix to the HTTP status it answers instead of the fixture.
  const failing = new Map<string, number>();
  const fn = (async (input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    for (const [prefix, code] of failing)
      if (url.startsWith(prefix))
        return new Response(JSON.stringify({ error: { message: `failed ${code}` } }), { status: code });
    let body: unknown = {};
    if (url.startsWith("/api/me"))
      body = { email: "dev@example.com", role, credential: "session", status_fresh: true };
    else if (url.startsWith("/api/status"))
      body = { inference: { state: "ok", reasons: [] }, models: [], version: "v", alerts: statusAlerts };
    else if (url.startsWith("/api/system")) body = { checks: { database: "ok", firewall: "ok" } };
    else if (url.startsWith("/api/alerts")) body = { enabled: true, alerts };
    else if (url.startsWith("/api/sessions")) body = { sessions: [] };
    return new Response(JSON.stringify(body), { status: 200 });
  }) as typeof fetch;
  return { fn, calls, failing, setAlerts: (a: ReturnType<typeof incident>[]) => (alerts = a) };
}

// stubLive records the topic handlers so a test can trigger them.
function stubLive() {
  const handlers = new Map<string, (() => void)[]>();
  const dropped: (() => void)[] = [];
  const state = { closed: 0 };
  const live = {
    on(topic: string, fn: () => void) {
      handlers.set(topic, [...(handlers.get(topic) ?? []), fn]);
      return () => {};
    },
    onDropped(fn: () => void) {
      dropped.push(fn);
      return () => {};
    },
    close() {
      state.closed++;
    },
  } as unknown as Live;
  return {
    live,
    state,
    fire: (topic: string) => (handlers.get(topic) ?? []).forEach((fn) => fn()),
    drop: () => dropped.forEach((fn) => fn()),
  };
}

async function shell(
  role: "admin" | "user",
  path = "/",
  statusAlerts: unknown[] = [],
  failAt: string[] = [],
) {
  const f = fakeFetch(role, statusAlerts);
  for (const prefix of failAt) f.failing.set(prefix, 503);
  const router = createAppRouter(createMemoryHistory());
  await router.push(path);
  const { live, fire, drop, state } = stubLive();
  const wrapper = mount(App, { props: { fetchFn: f.fn, live }, global: { plugins: [router] } });
  await flushPromises();
  return { wrapper, router, fire, drop, state, ...f };
}

const tabNames = (w: ReturnType<typeof mount>) =>
  w.findAll("nav[aria-label='Dashboard'] a").map((a) => a.text());

function memoryStorage(): Storage {
  const data = new Map<string, string>();
  return {
    get length() {
      return data.size;
    },
    clear: () => data.clear(),
    getItem: (k) => data.get(k) ?? null,
    key: (i) => [...data.keys()][i] ?? null,
    removeItem: (k) => void data.delete(k),
    setItem: (k, v) => void data.set(k, String(v)),
  };
}

describe("shell", () => {
  beforeEach(() => vi.stubGlobal("localStorage", memoryStorage()));

  it("shows a user only the Usage, Sessions and Keys tabs and lands on Usage", async () => {
    const { wrapper, router } = await shell("user");
    expect(tabNames(wrapper)).toEqual(["Usage", "Sessions", "Keys"]);
    expect(router.currentRoute.value.path).toBe("/usage");
  });

  it("redirects a user away from an admin route", async () => {
    const { router } = await shell("user", "/users");
    expect(router.currentRoute.value.path).toBe("/usage");
  });

  it.each(["/users", "/overview/spend"])("never mounts an admin view for a user at %s", async (path) => {
    const mounted: string[] = [];
    const Probe = defineComponent({
      setup() {
        mounted.push(useRoute().path);
        return () => null;
      },
    });
    const router = createRouter({
      history: createMemoryHistory(),
      routes: routes.map((r) => ({ ...r, component: Probe }) as RouteRecordRaw),
    });
    await router.push(path);
    const f = fakeFetch("user");
    const { live } = stubLive();
    mount(App, { props: { fetchFn: f.fn, live }, global: { plugins: [router] } });
    await flushPromises();
    expect(mounted).not.toContain(path);
    expect(router.currentRoute.value.path).toBe("/usage");
  });

  it("shows an administrator every tab with the open-alert count on Alerts", async () => {
    const { wrapper, router } = await shell("admin");
    expect(tabNames(wrapper)).toEqual(["Overview", "Usage", "Sessions", "Keys", "Users", "Alerts2"]);
    expect(router.currentRoute.value.path).toBe("/overview");
  });

  it("refetches the open-alert count on an alerts event", async () => {
    const { wrapper, fire, setAlerts } = await shell("admin");
    setAlerts([incident(1, null)]);
    fire("alerts");
    await flushPromises();
    expect(tabNames(wrapper)).toContain("Alerts1");
  });

  it("refetches the banner on a status event", async () => {
    // Sessions has no status widget of its own: the status calls here are the banner's and the top-bar
    // indicator's, one each per event.
    const { wrapper, fire, calls } = await shell("user", "/sessions");
    const before = calls.filter((u) => u.startsWith("/api/status")).length;
    expect(before).toBe(2);
    fire("status");
    await flushPromises();
    expect(calls.filter((u) => u.startsWith("/api/status")).length).toBe(before + 2);
    expect(wrapper.find("[data-banner]").exists()).toBe(false);
  });

  it("renders the status alerts in the banner", async () => {
    const { wrapper } = await shell("user", "/", [
      { key: "provider:openrouter", severity: "critical", message: "OpenRouter is unreachable" },
    ]);
    expect(wrapper.find("[data-banner]").text()).toContain("OpenRouter is unreachable");
  });

  it("offers GitLab sign-in without a session", async () => {
    const router = createAppRouter(createMemoryHistory());
    await router.push("/");
    const fn = (async () =>
      new Response(JSON.stringify({ error: { message: "unauthenticated" } }), {
        status: 401,
      })) as typeof fetch;
    const wrapper = mount(App, {
      props: { fetchFn: fn, live: stubLive().live },
      global: { plugins: [router] },
    });
    await flushPromises();
    const button = wrapper.find("a[href='/auth/gitlab/login']");
    expect(button.exists()).toBe(true);
    expect(button.text()).toBe("Sign in with GitLab");
    expect(button.find("svg").exists()).toBe(true);
    expect(wrapper.find(".signin h1").text()).toBe("Wrench");
    expect(wrapper.find(".signin").text()).toContain("Private team coding agent");
    expect(button.classes()).toEqual(expect.arrayContaining(["button", "primary"]));
    expect(wrapper.find("nav").exists()).toBe(false);
  });

  it("shows a sign-in failure inside the sign-in card", async () => {
    const router = createAppRouter(createMemoryHistory());
    await router.push("/");
    const fn = (async () =>
      new Response(JSON.stringify({ error: { message: "database unavailable" } }), {
        status: 503,
      })) as typeof fetch;
    const wrapper = mount(App, {
      props: { fetchFn: fn, live: stubLive().live },
      global: { plugins: [router] },
    });
    await flushPromises();
    expect(wrapper.find(".signin [role='alert']").text()).toBe("database unavailable");
    expect(wrapper.findAll("[role='alert']")).toHaveLength(1);
  });

  it("shows the status indicator left of the email in the top bar", async () => {
    const { wrapper } = await shell("user", "/usage");
    const bar = wrapper.find("header.bar");
    const indicator = bar.find("[data-status-indicator]");
    expect(indicator.attributes("title")).toBe("Running");
    const html = bar.html();
    expect(html.indexOf("data-status-indicator")).toBeLessThan(html.indexOf("dev@example.com"));
  });

  it("reads Unknown for an administrator while the system checks fail", async () => {
    const { wrapper, fire, failing } = await shell("admin", "/usage");
    const pill = () => wrapper.find("[data-status-indicator]").attributes("title") ?? "";
    expect(pill()).toContain("Running");
    failing.set("/api/system", 503);
    fire("status");
    await flushPromises();
    expect(pill()).toContain("Unknown");
    failing.delete("/api/system");
    fire("status");
    await flushPromises();
    expect(pill()).toContain("Running");
  });

  it("reads Unknown on the first load when the system checks fail", async () => {
    const { wrapper } = await shell("admin", "/usage", [], ["/api/system"]);
    expect(wrapper.find("[data-status-indicator]").attributes("title")).toContain("Unknown");
  });

  it("links the brand and logo to the start page", async () => {
    const { wrapper } = await shell("user", "/sessions");
    const brand = wrapper.find("header a.brand");
    expect(brand.attributes("href")).toBe("/");
    expect(brand.text()).toBe("Wrench");
    expect(brand.find("svg").exists()).toBe(true);
  });

  it("shows the signed-out state when the stream drops and the session is gone", async () => {
    const { wrapper, drop, failing, state } = await shell("user", "/sessions");
    failing.set("/api/me", 401);
    drop();
    await flushPromises();
    expect(wrapper.find("a[href='/auth/gitlab/login']").exists()).toBe(true);
    expect(wrapper.find("nav").exists()).toBe(false);
    expect(state.closed).toBeGreaterThan(0);
  });

  it("stays signed in when the stream drops during an outage", async () => {
    const { wrapper, drop, failing } = await shell("user", "/sessions");
    failing.set("/api/me", 502);
    drop();
    await flushPromises();
    expect(wrapper.find("nav").exists()).toBe(true);
  });

  it("clears a shell error after a later success", async () => {
    const { wrapper, fire, failing } = await shell("user", "/sessions");
    failing.set("/api/status", 503);
    fire("status");
    await flushPromises();
    expect(wrapper.find("main > .error").exists()).toBe(true);
    failing.delete("/api/status");
    fire("status");
    await flushPromises();
    expect(wrapper.find("main > .error").exists()).toBe(false);
  });

  it("sends an unknown path to the landing tab", async () => {
    const { router } = await shell("admin", "/no/such/page");
    expect(router.currentRoute.value.path).toBe("/overview");
  });

  it("renders the device page without the shell", async () => {
    const { wrapper } = await shell("user", "/device?code=ABCD-EFGH");
    expect(wrapper.find("nav").exists()).toBe(false);
    expect(wrapper.text()).toContain("ABCD-EFGH");
  });
});

describe("alert banner", () => {
  const stubs = { RouterLink: { template: "<a><slot /></a>" } };
  it("renders every message and nothing when there are none", () => {
    const some = mount(AlertBanner, {
      props: {
        alerts: [
          { key: "a", severity: "critical", message: "first" },
          { key: "b", severity: "critical", message: "second" },
        ],
        admin: true,
      },
      global: { stubs },
    });
    expect(some.text()).toContain("first");
    expect(some.text()).toContain("second");
    const none = mount(AlertBanner, { props: { alerts: [], admin: false }, global: { stubs } });
    expect(none.find("[data-banner]").exists()).toBe(false);
  });
});

describe("theme", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
    document.documentElement.removeAttribute("data-theme");
  });

  it("follows prefers-color-scheme on the first visit", () => {
    vi.stubGlobal("matchMedia", () => ({ matches: false }));
    initTheme();
    expect(document.documentElement.dataset.theme).toBe("light");
  });

  it("follows a dark system preference", () => {
    vi.stubGlobal("matchMedia", () => ({ matches: true }));
    initTheme();
    expect(document.documentElement.dataset.theme).toBe("dark");
  });

  it("remembers the toggle and prefers it over the system", () => {
    vi.stubGlobal("matchMedia", () => ({ matches: true }));
    initTheme();
    toggleTheme();
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(localStorage.getItem("wrench.theme")).toBe("light");
    initTheme();
    expect(document.documentElement.dataset.theme).toBe("light");
  });

  it("survives unavailable storage", () => {
    vi.stubGlobal("localStorage", {
      getItem: () => {
        throw new Error("blocked");
      },
      setItem: () => {
        throw new Error("blocked");
      },
    });
    vi.stubGlobal("matchMedia", () => ({ matches: true }));
    initTheme();
    expect(() => toggleTheme()).not.toThrow();
    expect(document.documentElement.dataset.theme).toBe("light");
  });
});
