import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createMemoryHistory } from "vue-router";
import App from "./App.vue";
import { createAppRouter } from "./router";
import type { Live } from "./live";
import type { ClientKey, Incident, Session, UserRow } from "./api";

// Native dialogs are not implemented by jsdom.
function stubDialog() {
  const proto = HTMLDialogElement.prototype as unknown as Record<string, unknown>;
  const saved = { showModal: proto.showModal, close: proto.close };
  proto.showModal = function (this: HTMLDialogElement) {
    this.setAttribute("open", "");
  };
  proto.close = function (this: HTMLDialogElement) {
    if (!this.hasAttribute("open")) return;
    this.removeAttribute("open");
    this.dispatchEvent(new Event("close"));
  };
  return () => Object.assign(proto, saved);
}

const tokens = { input: 1, output: 1, reasoning: null, cache_read: null, cache_write: null };
const session = (id: string, o: Partial<Session> & { pending?: number } = {}): Session =>
  ({
    id,
    user_email: "a@x",
    device_id: "d1",
    device_name: "Laptop",
    agent: "opencode/2.3.4",
    status: "active",
    started_at: "2026-11-09T10:00:00Z",
    last_seen_at: "2026-11-09T10:42:00Z",
    usage: { attempts: 7, pending: o.pending ?? 0, cost: "0.5", tokens },
    ...o,
  }) as unknown as Session;

const key = (id: number, o: Partial<ClientKey> = {}): ClientKey => ({
  id,
  user_email: "a@x",
  device_id: `dev${id}`,
  device_name: `Device ${id}`,
  created_at: "2026-11-09T10:00:00Z",
  expires_at: "2999-01-01T00:00:00Z",
  last_used_at: null,
  revoked_at: null,
  ...o,
});

async function harness(
  role: "admin" | "user",
  path: string,
  data: { sessions?: Session[]; keys?: ClientKey[]; users?: UserRow[]; alerts?: Incident[] } = {},
) {
  const calls: { url: string; method: string }[] = [];
  const fn = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({ url, method: init?.method ?? "GET" });
    const p = url.split("?")[0]!;
    if (p === "/api/me")
      return new Response(JSON.stringify({ email: "me@x", role, credential: "session", status_fresh: true }));
    if (p === "/api/status")
      return new Response(
        JSON.stringify({ inference: { state: "ok", reasons: [] }, models: [], version: "v", alerts: [] }),
      );
    if (p === "/api/alerts")
      return new Response(JSON.stringify({ enabled: true, alerts: data.alerts ?? [] }));
    if (p === "/api/budget") return new Response(JSON.stringify({ windows: [], warn_at_percent: [80, 95] }));
    if (p === "/api/sessions") return new Response(JSON.stringify({ sessions: data.sessions ?? [] }));
    if (p === "/api/keys") return new Response(JSON.stringify({ keys: data.keys ?? [] }));
    if (p === "/api/users")
      return new Response(
        JSON.stringify({ users: data.users ?? [{ email: "me@x" }, { email: "a@x" }, { email: "b@x" }] }),
      );
    if (p.startsWith("/api/users/")) return new Response(null, { status: 204 });
    if (p === "/api/keys/revoke") return new Response(JSON.stringify({ revoked: 2 }));
    return new Response("{}");
  }) as typeof fetch;
  const live = { on: () => () => {}, onDropped: () => () => {}, close() {} } as unknown as Live;
  const router = createAppRouter(createMemoryHistory());
  await router.push(path);
  const wrapper = mount(App, { props: { fetchFn: fn, live }, global: { plugins: [router] } });
  await flushPromises();
  return { wrapper, calls, router };
}

const headers = (w: { findAll: (s: string) => { text: () => string }[] }) =>
  w.findAll("table thead th").map((h) => h.text());

describe("sessions", () => {
  it("shows the columns in order for an administrator, without Unconfirmed when nothing is pending", async () => {
    const { wrapper } = await harness("admin", "/sessions", { sessions: [session("s1")] });
    expect(headers(wrapper)).toEqual([
      "Status",
      "Session",
      "User",
      "Agent",
      "Device",
      "Started",
      "Last seen",
      "Duration",
      "Requests",
      "Cost",
    ]);
  });

  it("hides User for a user and adds Unconfirmed with its tooltip when a row has pending requests", async () => {
    const { wrapper } = await harness("user", "/sessions", { sessions: [session("s1", { pending: 2 })] });
    const h = headers(wrapper);
    expect(h).not.toContain("User");
    expect(h[h.length - 1]).toBe("Unconfirmed");
    const th = wrapper.findAll("th").find((t) => t.text() === "Unconfirmed")!;
    expect(th.attributes("title")).toBe("Requests whose final price OpenRouter has not confirmed yet");
  });

  it("marks Last seen as sorted descending and uses num cells", async () => {
    const { wrapper } = await harness("user", "/sessions", { sessions: [session("s1")] });
    const th = wrapper.findAll("th").find((t) => t.text() === "Last seen")!;
    expect(th.attributes("aria-sort")).toBe("descending");
    const cells = wrapper.findAll("tbody td");
    expect(cells.find((c) => c.text() === "7")!.classes()).toContain("num");
  });

  it("trims the session id, keeps the full id in the title and copies it", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const id = "0123456789abcdefXYZ";
    const { wrapper } = await harness("user", "/sessions", { sessions: [session(id)] });
    const idCell = wrapper.find("tbody td .session-id");
    expect(idCell.text()).toBe("0123456789ab");
    expect(idCell.attributes("title")).toBe(id);
    await wrapper.find("button[aria-label='Copy session id']").trigger("click");
    expect(writeText).toHaveBeenCalledWith(id);
  });

  it("renders an unknown agent as a dash and a known one verbatim", async () => {
    const { wrapper } = await harness("user", "/sessions", {
      sessions: [session("s1", { agent: "" }), session("s2")],
    });
    const rows = wrapper.findAll("tbody tr");
    expect(rows[0]!.text()).toContain("—");
    expect(rows[1]!.text()).toContain("opencode/2.3.4");
  });

  it("shows status pill and duration", async () => {
    const { wrapper } = await harness("user", "/sessions", { sessions: [session("s1")] });
    expect(wrapper.find("tbody .pill").text()).toContain("active");
    expect(wrapper.find("tbody").text()).toContain("42 min");
  });

  it("writes the status filter to the query and requests it", async () => {
    const { wrapper, calls, router } = await harness("user", "/sessions");
    expect(calls.some((c) => c.url === "/api/sessions?limit=50")).toBe(true);
    await wrapper.find("select[aria-label='Status']").setValue("offline");
    await flushPromises();
    expect(router.currentRoute.value.query.status).toBe("offline");
    expect(calls.some((c) => c.url === "/api/sessions?limit=50&status=offline")).toBe(true);
  });

  it("selects Offline from ?status=offline on load", async () => {
    const { wrapper, calls } = await harness("user", "/sessions?status=offline");
    expect((wrapper.find("select[aria-label='Status']").element as HTMLSelectElement).value).toBe("offline");
    expect(calls.some((c) => c.url === "/api/sessions?limit=50&status=offline")).toBe(true);
  });

  it("lets an administrator pick a user, defaulting to Me", async () => {
    const me = await harness("admin", "/sessions");
    expect(me.calls.some((c) => c.url === "/api/sessions?limit=50")).toBe(true);
    const picked = await harness("admin", "/sessions?user=b@x");
    expect((picked.wrapper.find("select[aria-label='User']").element as HTMLSelectElement).value).toBe("b@x");
    expect(picked.calls.some((c) => c.url === "/api/sessions?limit=50&user=b%40x")).toBe(true);
  });

  it("gives a user no user select", async () => {
    const { wrapper } = await harness("user", "/sessions");
    expect(wrapper.find("select[aria-label='User']").exists()).toBe(false);
  });

  it("shows a down arrow next to the sorted Last seen header", async () => {
    const { wrapper } = await harness("user", "/sessions", { sessions: [session("s1")] });
    const th = wrapper.findAll("th").find((t) => t.text() === "Last seen")!;
    const icon = th.find("svg.sort-icon");
    expect(icon.exists()).toBe(true);
    expect(icon.attributes("aria-hidden")).toBe("true");
    expect(wrapper.findAll("th svg.sort-icon")).toHaveLength(1);
  });

  it("puts the session id and an icon-only copy button side by side in one cell", async () => {
    const { wrapper } = await harness("user", "/sessions", { sessions: [session("0123456789abcdef")] });
    const cell = wrapper.find("tbody td .id-cell");
    expect(cell.exists()).toBe(true);
    expect(cell.find(".session-id").exists()).toBe(true);
    const button = cell.find("button.copy[aria-label='Copy session id']");
    expect(button.find("svg").exists()).toBe(true);
    expect(button.text()).toBe("");
  });

  it("rounds the cost to cents, marks sub-cent amounts and keeps the exact value in the title", async () => {
    const sessions = [
      session("s1", { usage: { attempts: 1, pending: 0, cost: "0.0589218", tokens } } as Partial<Session>),
      session("s2", { usage: { attempts: 1, pending: 0, cost: "0.0004", tokens } } as Partial<Session>),
      session("s3", { usage: { attempts: 1, pending: 0, cost: "0", tokens } } as Partial<Session>),
    ];
    const { wrapper } = await harness("user", "/sessions", { sessions });
    const costs = wrapper.findAll("tbody td.cost");
    expect(costs.map((c) => c.text())).toEqual(["$0.06", "<$0.01", "$0.00"]);
    expect(costs[0]!.attributes("title")).toBe("$0.0589218");
  });
});

describe("keys", () => {
  let restore: () => void;
  beforeEach(() => (restore = stubDialog()));
  afterEach(() => restore());

  const command = () => `opencode auth login opencode --method device --answer server=${location.origin}`;

  it("explains an empty list with the full opencode login command", async () => {
    const { wrapper } = await harness("user", "/keys");
    expect(wrapper.text()).toContain("No device keys. Sign in from opencode with");
    expect(wrapper.find("table.keys, p code").text()).toBe(command());
  });

  it("opens a how-to dialog from the primary New device key button", async () => {
    const { wrapper } = await harness("user", "/keys", { keys: [key(1)] });
    const button = wrapper.findAll("button").find((b) => b.text() === "New device key")!;
    expect(button.classes()).toContain("primary");
    expect(wrapper.find("dialog[open]").exists()).toBe(false);
    await button.trigger("click");
    const dialog = wrapper.find("dialog[open]");
    expect(dialog.exists()).toBe(true);
    expect(dialog.find("code").text()).toBe(command());
    expect(dialog.text()).toContain("Install opencode");
    expect(dialog.text()).toContain("/device");
    expect(dialog.find(".copy-button").exists()).toBe(true);
    await dialog.find("button[data-close]").trigger("click");
    expect(wrapper.find("dialog[open]").exists()).toBe(false);
  });

  it("lays the how-to out as a wide dialog with numbered steps and a full-width command block", async () => {
    const { wrapper } = await harness("user", "/keys", { keys: [key(1)] });
    await wrapper
      .findAll("button")
      .find((b) => b.text() === "New device key")!
      .trigger("click");
    const dialog = wrapper.find("dialog[open]");
    expect(dialog.classes()).toContain("wide");
    expect(dialog.find("h2 svg").exists()).toBe(true);
    expect(dialog.findAll(".step .step-num").map((n) => n.text())).toEqual(["1", "2", "3"]);
    const block = dialog.find(".command-block");
    expect(block.find("code").text()).toBe(command());
    // Copy sits in a bar above the command, so a long command never pushes it out of view.
    const parts = block.element.children;
    expect(parts[0]!.classList.contains("command-bar")).toBe(true);
    expect(parts[0]!.querySelector(".copy-button")).not.toBeNull();
    expect(parts[1]!.tagName).toBe("CODE");
    expect(dialog.find("button[data-close]").classes()).toContain("primary");
  });

  it.each([
    ["", true],
    ["*", false],
    ["b@x", false],
  ])("shows New device key to an administrator only for Me (user=%s)", async (user, shown) => {
    const path = user ? `/keys?user=${encodeURIComponent(user)}` : "/keys";
    const { wrapper } = await harness("admin", path, { keys: [key(1)] });
    expect(wrapper.findAll("button").some((b) => b.text() === "New device key")).toBe(shown);
  });

  it("keeps Revoke in a fixed-width last column as a compact button", async () => {
    const { wrapper } = await harness("user", "/keys", { keys: [key(1)] });
    const cell = wrapper.find("table.keys tbody tr td:last-child");
    expect(cell.classes()).toContain("row-action");
    expect(cell.find("button").classes()).toEqual(expect.arrayContaining(["danger", "small"]));
    expect(wrapper.find("table.keys thead th:last-child").classes()).toContain("row-action");
  });

  it("asks before revoking a key; cancel does nothing, confirm revokes once", async () => {
    const { wrapper, calls } = await harness("user", "/keys", { keys: [key(1)] });
    const revoke = () => wrapper.findAll("table.keys button").find((b) => b.text() === "Revoke")!;
    expect(revoke().classes()).toContain("danger");
    await revoke().trigger("click");
    const dialog = wrapper.find("dialog.confirm");
    expect(dialog.attributes("open")).toBeDefined();
    expect(dialog.text()).toContain("Device 1");
    await dialog.find("button[data-cancel]").trigger("click");
    expect(calls.filter((c) => c.method === "DELETE")).toHaveLength(0);
    await revoke().trigger("click");
    await wrapper.find("dialog.confirm button.danger").trigger("click");
    await flushPromises();
    expect(calls.filter((c) => c.method === "DELETE" && c.url === "/api/keys/1")).toHaveLength(1);
  });

  it("revokes all active keys after confirming with the count", async () => {
    const { wrapper, calls } = await harness("user", "/keys", {
      keys: [key(1), key(2), key(3, { revoked_at: "2026-11-09T11:00:00Z" })],
    });
    const revokeAll = wrapper.findAll("button").find((b) => b.text() === "Revoke all")!;
    expect(revokeAll.classes()).toContain("danger");
    await revokeAll.trigger("click");
    expect(wrapper.find("dialog.confirm").text()).toContain("2");
    await wrapper.find("dialog.confirm button.danger").trigger("click");
    await flushPromises();
    expect(calls.filter((c) => c.method === "POST" && c.url === "/api/keys/revoke")).toHaveLength(1);
  });

  it("revokes the selected user's keys for an administrator", async () => {
    const { wrapper, calls } = await harness("admin", "/keys?user=b@x", { keys: [key(1)] });
    await wrapper
      .findAll("button")
      .find((b) => b.text() === "Revoke all")!
      .trigger("click");
    await wrapper.find("dialog.confirm button.danger").trigger("click");
    await flushPromises();
    expect(calls.some((c) => c.method === "POST" && c.url === "/api/keys/revoke?user=b%40x")).toBe(true);
  });

  it("hides Revoke all for All users and when no key is active", async () => {
    const all = await harness("admin", "/keys?user=*", { keys: [key(1)] });
    expect(all.wrapper.findAll("button").some((b) => b.text() === "Revoke all")).toBe(false);
    const none = await harness("user", "/keys", { keys: [key(1, { revoked_at: "2026-11-09T11:00:00Z" })] });
    expect(none.wrapper.findAll("button").some((b) => b.text() === "Revoke all")).toBe(false);
  });

  it("has no Revoke all for user button and defaults the admin to Me", async () => {
    const { wrapper, calls } = await harness("admin", "/keys", { keys: [key(1)] });
    expect(wrapper.text()).not.toContain("Revoke all for user");
    expect(calls.some((c) => c.url === "/api/keys")).toBe(true);
  });

  it("shows state as a pill", async () => {
    const { wrapper } = await harness("user", "/keys", { keys: [key(1)] });
    expect(wrapper.find("table.keys .pill").text()).toContain("active");
  });
});

const win = (window: string, percent: number, spent: string) => ({
  scope: "user",
  window,
  spent,
  limit: "1",
  percent,
  resets_at: "2026-11-10T00:00:00Z",
});
const userRow = (email: string, usage: unknown[]): UserRow =>
  ({
    email,
    role: "user",
    tier: "default",
    limits: {},
    state: { email, status: "active", active_keys: 2, status_checked_at: null, last_key_use_at: null },
    usage,
  }) as unknown as UserRow;

// Every button carries an icon beside its label, so actions read at a glance across the dashboard.
describe("button icons", () => {
  let restore: () => void;
  beforeEach(() => (restore = stubDialog()));
  afterEach(() => restore());

  it.each<[string, Parameters<typeof harness>[2]]>([
    ["/keys", { keys: [key(1)] }],
    ["/users", { users: [userRow("a@x", [])] }],
  ])("gives every button on %s an icon", async (path, data) => {
    const { wrapper } = await harness("admin", path, data);
    const buttons = wrapper.findAll("button");
    expect(buttons.length).toBeGreaterThan(3);
    for (const b of buttons) expect(b.find("svg").exists(), b.text() || b.html()).toBe(true);
  });
});

describe("users", () => {
  let restore: () => void;
  beforeEach(() => (restore = stubDialog()));
  afterEach(() => restore());

  it("derives one column per window present in the data, in window order", async () => {
    const users = [
      userRow("a@x", [win("month", 10, "1"), win("day", 50, "2")]),
      userRow("b@x", [win("5_hours", 1, "0")]),
    ];
    const { wrapper } = await harness("admin", "/users", { users });
    expect(headers(wrapper)).toEqual([
      "User",
      "Role",
      "Tier",
      "State",
      "Active keys",
      "Last key use",
      "5 hours",
      "day",
      "month",
      "",
    ]);
  });

  it("shows percent, amount and a bar colored by the thresholds", async () => {
    const users = [userRow("a@x", [win("5_hours", 50, "1"), win("day", 85, "2"), win("week", 100, "3")])];
    const { wrapper } = await harness("admin", "/users", { users });
    const cells = wrapper.findAll("tbody td.spend");
    expect(cells).toHaveLength(3);
    expect(cells[0]!.text()).toContain("50%");
    expect(cells[0]!.text()).toContain("$1.00");
    expect(cells[0]!.find(".fill").classes()).not.toContain("warn");
    expect(cells[1]!.find(".fill").classes()).toContain("warn");
    expect(cells[2]!.find(".fill").classes()).toContain("bad");
  });

  it("renders a window a user has not used as a dash and the state as a pill", async () => {
    const users = [userRow("a@x", [win("day", 5, "0")]), userRow("b@x", [win("week", 5, "0")])];
    const { wrapper } = await harness("admin", "/users", { users });
    expect(wrapper.find("tbody tr").findAll("td.spend")[1]!.text()).toBe("—");
    expect(wrapper.find("tbody .pill").text()).toContain("active");
  });

  it("marks a user who never signed in and offers them no Revoke", async () => {
    const users = [{ ...userRow("n@x", []), state: null } as unknown as UserRow, userRow("a@x", [])];
    const { wrapper } = await harness("admin", "/users", { users });
    const [never, active] = wrapper.findAll("tbody tr");
    const pill = never!.find(".pill");
    expect(pill.text()).toBe("never signed in");
    expect(pill.classes()).toContain("muted");
    expect(never!.findAll("button").filter((b) => b.text() === "Revoke all keys")).toHaveLength(0);
    const revoke = active!.findAll("button").filter((b) => b.text() === "Revoke all keys");
    expect(revoke).toHaveLength(1);
    expect(revoke[0]!.classes()).toContain("danger");
  });

  it("shows the last key use as a time cell and spend as a right-aligned numeric block", async () => {
    const used = userRow("a@x", [win("day", 5, "1")]);
    (used.state as { last_key_use_at: string | null }).last_key_use_at = "2026-11-09T10:00:00Z";
    const { wrapper } = await harness("admin", "/users", { users: [used] });
    const time = wrapper.find("tbody .time");
    expect(time.exists()).toBe(true);
    expect(time.attributes("title")).toBe("2026-11-09T10:00:00Z");
    const spend = wrapper.find("tbody td.spend");
    expect(spend.classes()).toContain("num");
    expect(spend.find(".figures").exists()).toBe(true);
  });

  it("revokes only after confirming the dialog naming the user", async () => {
    const { wrapper, calls } = await harness("admin", "/users", { users: [userRow("a@x", [])] });
    const revoke = () => wrapper.findAll("tbody button").find((b) => b.text() === "Revoke all keys")!;
    await revoke().trigger("click");
    expect(wrapper.find("dialog").text()).toContain("Revoke every key and session of a@x?");
    await wrapper.find("dialog button[data-cancel]").trigger("click");
    expect(calls.filter((c) => c.url === "/api/users/a%40x/revoke")).toHaveLength(0);
    await revoke().trigger("click");
    await wrapper.find("dialog button.danger").trigger("click");
    await flushPromises();
    expect(calls.filter((c) => c.method === "POST" && c.url === "/api/users/a%40x/revoke")).toHaveLength(1);
  });
});

const incident = (id: number, o: Partial<Incident> = {}): Incident => ({
  id,
  key: `k${id}`,
  severity: "warning",
  user_email: "a@x",
  message: `message ${id}`,
  opened_at: "2026-11-09T10:00:00Z",
  last_seen_at: "2026-11-09T10:30:00Z",
  last_notified_at: null,
  resolved_at: null,
  resolve_notified: false,
  ...o,
});

describe("alerts", () => {
  let restore: () => void;
  beforeEach(() => (restore = stubDialog()));
  afterEach(() => restore());
  const list = [
    incident(1, { severity: "critical" }),
    incident(2, { severity: "warning" }),
    incident(3, { severity: "critical", resolved_at: "2026-11-09T11:00:00Z" }),
    incident(4, { severity: "warning", resolved_at: "2026-11-09T11:00:00Z" }),
  ];
  const ids = (w: { findAll: (s: string) => { text: () => string }[] }) =>
    w.findAll("tbody td.message").map((c) => c.text());

  it("hides resolved incidents by default and shows Open pills", async () => {
    const { wrapper } = await harness("admin", "/alerts", { alerts: list });
    expect(ids(wrapper)).toEqual(["message 1", "message 2"]);
    expect(wrapper.find("tbody td.state .pill").text()).toContain("Open");
  });

  it("filters by severity and status from the query", async () => {
    const { wrapper } = await harness("admin", "/alerts?status=all&severity=critical", { alerts: list });
    expect(ids(wrapper)).toEqual(["message 1", "message 3"]);
    const rows = wrapper.findAll("tbody tr");
    expect(rows[0]!.classes()).not.toContain("resolved");
    expect(rows[1]!.classes()).toContain("resolved");
    expect(rows[1]!.find("td.state .pill").text()).toContain("Resolved");
  });

  it("writes filter changes to the query", async () => {
    const { wrapper, router } = await harness("admin", "/alerts", { alerts: list });
    await wrapper.find("select[aria-label='Severity']").setValue("warning");
    await flushPromises();
    await wrapper.find("select[aria-label='Status']").setValue("resolved");
    await flushPromises();
    expect(router.currentRoute.value.query).toMatchObject({ severity: "warning", status: "resolved" });
    expect(ids(wrapper)).toEqual(["message 4"]);
  });

  it("opens a details dialog on a row click and leaves the row unexpanded", async () => {
    const full = incident(1, {
      severity: "critical",
      user_email: "u@x",
      message: "the full message",
      key: "budget:user:u@x:day",
      opened_at: "2026-11-09T10:00:00Z",
      last_seen_at: "2026-11-09T10:30:00Z",
      resolved_at: "2026-11-09T11:00:00Z",
    });
    const { wrapper } = await harness("admin", "/alerts?status=all", { alerts: [full] });
    expect(wrapper.find("dialog[open]").exists()).toBe(false);
    await wrapper.find("tbody tr").trigger("click");
    const dialog = wrapper.find("dialog[open]");
    expect(dialog.exists()).toBe(true);
    const text = dialog.text();
    for (const part of [
      "critical",
      "Resolved",
      "u@x",
      "the full message",
      "budget:user:u@x:day",
      "2026-11-09 10:00:00 UTC",
      "2026-11-09 10:30:00 UTC",
      "2026-11-09 11:00:00 UTC",
    ])
      expect(text).toContain(part);
    for (const label of ["Severity", "Status", "User", "Message", "Key", "Opened", "Last seen", "Resolved"])
      expect(text).toContain(label);
    expect(wrapper.find("tbody tr").classes()).not.toContain("expanded");
    expect(wrapper.find("tbody tr").attributes("aria-expanded")).toBeUndefined();
  });

  it("opens the dialog from the keyboard, ignores other keys and closes on Escape", async () => {
    const { wrapper } = await harness("admin", "/alerts", { alerts: list });
    const row = () => wrapper.find("tbody tr");
    expect(row().attributes("tabindex")).toBe("0");
    await row().trigger("keydown", { key: "a" });
    expect(wrapper.find("dialog[open]").exists()).toBe(false);
    await row().trigger("keydown", { key: "Enter" });
    expect(wrapper.find("dialog[open]").text()).toContain("message 1");
    await wrapper.find("dialog").trigger("cancel");
    expect(wrapper.find("dialog[open]").exists()).toBe(false);
    await row().trigger("keydown", { key: " " });
    expect(wrapper.find("dialog[open]").exists()).toBe(true);
  });

  it("shows an open incident's status as Open", async () => {
    const { wrapper } = await harness("admin", "/alerts", { alerts: list });
    await wrapper.find("tbody tr").trigger("click");
    expect(wrapper.findAll("dialog[open] .pill")[1]!.text()).toContain("Open");
  });

  it("renders severity as a pill", async () => {
    const { wrapper } = await harness("admin", "/alerts", { alerts: list });
    const [critical, warning] = wrapper.findAll("tbody tr").map((r) => r.find("td.severity .pill"));
    expect(critical!.text()).toBe("critical");
    expect(critical!.classes()).toContain("bad");
    expect(warning!.text()).toBe("warning");
    expect(warning!.classes()).toContain("warn");
  });

  it("shows compact dates with the full UTC time in the title", async () => {
    const { wrapper } = await harness("admin", "/alerts", {
      alerts: [incident(1, { opened_at: "2026-11-19T22:51:07Z" })],
    });
    const opened = wrapper.find("tbody tr").findAll(".time")[0]!;
    expect(opened.find(".mono").text()).toBe("Nov 19, 22:51");
    expect(opened.attributes("title")).toBe("2026-11-19 22:51:07 UTC");
    expect(opened.find(".rel").exists()).toBe(true);
  });

  it("uses the fixed-layout table class", async () => {
    const { wrapper } = await harness("admin", "/alerts", { alerts: list });
    expect(wrapper.find("table.alerts").exists()).toBe(true);
  });
});
