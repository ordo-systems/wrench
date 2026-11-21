// Types of the API's JSON, generated from api/openapi.yaml (make generate). Nullable numbers are
// unreported values, never zero.
import { inject, type InjectionKey, type Ref } from "vue";
import type { components } from "./api.gen.ts";

type Schemas = components["schemas"];

export type Tokens = Schemas["Tokens"];
export type CostState = Schemas["Attempt"]["cost_state"];
export type Attempt = Schemas["Attempt"];
export type UsageTotals = Schemas["UsageTotals"];
export type Usage = Schemas["Usage"];
/** Sum over attempts that reported a field; sum is null when none did. */
export type Observed = Schemas["Observed"];
export type Latency = Schemas["Latency"];
/** Aggregates of one time bucket, or of the whole period for totals; see GET /api/stats. */
export type StatsBucket = Schemas["StatsBucket"];
export type Breakdown = Schemas["Breakdown"];
export type Stats = Schemas["Stats"];
export type UserRow = Schemas["UserRow"];
export type ModelInfo = Schemas["SystemModel"];
export type ModelList = Schemas["ModelList"];
export type TierInfo = Schemas["Tier"];
export type SystemInfo = Schemas["System"];
export type Incident = Schemas["Incident"];
/** Live status from requests; "offline" means the client went silent, not that it stopped. */
export type SessionStatus = Schemas["Session"]["status"];
export type Session = Schemas["Session"];
export type Identity = Schemas["Identity"];
export type ClientKey = Schemas["ClientKey"];
export type Window = Schemas["Window"];
export type BudgetWindow = Schemas["BudgetWindow"];
export type BudgetStatus = Schemas["BudgetStatus"];
/** Whether the caller's inference can run; paused exactly when reasons is not empty. */
export type Status = Schemas["Status"];
export type PauseReason = Schemas["PauseReason"];
/** Whose data a list returns: "" for the caller, "*" for everyone or an email; only admins may pass the others. */
export type Scope = string;

// scoped appends ?user= (or &user=) when a scope is given; without it the server answers for the caller.
const scoped = (path: string, user: Scope = "") =>
  user ? `${path}${path.includes("?") ? "&" : "?"}user=${encodeURIComponent(user)}` : path;

export type Period = "24h" | "7d" | "30d";

export interface StatsQuery {
  period: Period;
  model: string;
  user: string;
  /** "user" adds the per-user series (administrators only). */
  groupBy?: "user";
}

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

// Client calls the dashboard API with the HttpOnly session cookie set by GitLab sign-in.
export class Client {
  constructor(private fetchFn: typeof fetch = (...args) => fetch(...args)) {}

  private async call(method: string, path: string, body?: string): Promise<Response> {
    const headers: Record<string, string> = {};
    if (body !== undefined) headers["Content-Type"] = "application/json";
    const resp = await this.fetchFn(path, { method, headers, body, credentials: "same-origin" });
    if (!resp.ok) {
      let message = resp.statusText;
      try {
        message = (await resp.json()).error?.message ?? message;
      } catch {
        // keep the status text
      }
      throw new ApiError(resp.status, message);
    }
    return resp;
  }

  private async get<T>(path: string): Promise<T> {
    return (await (await this.call("GET", path)).json()) as T;
  }

  me(): Promise<Identity> {
    return this.get("/api/me");
  }

  /** usage totals a window, grouped by user, session or model (the group key is then the model name). */
  usage(window: Window, group: "" | "user" | "session" | "model" = "", user: Scope = ""): Promise<Usage> {
    return this.get(scoped(`/api/usage?window=${window}&group=${group}`, user));
  }

  status(): Promise<Status> {
    return this.get("/api/status");
  }

  /** stats loads chart aggregates; the period picks the bucket width (5 min, hour or day). */
  stats(q: StatsQuery, now = Date.now()): Promise<{ bucket_seconds: number; stats: Stats }> {
    const hour = 3_600_000;
    const [ms, bucket, width] = {
      "24h": [24 * hour, "hour", hour],
      "7d": [7 * 24 * hour, "hour", hour],
      "30d": [30 * 24 * hour, "day", 24 * hour],
    }[q.period] as [number, string, number];
    // Buckets align to the start, so start on a bucket boundary.
    const from = Math.floor((now - ms) / width) * width;
    const p = new URLSearchParams({ bucket, from: new Date(from).toISOString().slice(0, 19) + "Z" });
    if (q.model) p.set("model", q.model);
    if (q.user) p.set("user", q.user);
    if (q.groupBy) p.set("group_by", q.groupBy);
    return this.get(`/api/stats?${p}`);
  }

  /** models lists the models of the caller's tier and its default. */
  models(): Promise<ModelList> {
    return this.get("/api/models");
  }

  async users(): Promise<UserRow[]> {
    return (await this.get<{ users: UserRow[] }>("/api/users")).users;
  }

  system(): Promise<SystemInfo> {
    return this.get("/api/system");
  }

  async alerts(user: Scope = ""): Promise<{ alerts: Incident[]; enabled: boolean }> {
    return this.get(scoped("/api/alerts?limit=50", user));
  }

  /** sessions lists sessions by last seen, newest first; status narrows them to active or offline ones. */
  async sessions(limit = 50, user: Scope = "", status?: SessionStatus): Promise<Session[]> {
    const path = `/api/sessions?limit=${limit}${status ? `&status=${status}` : ""}`;
    return (await this.get<{ sessions: Session[] }>(scoped(path, user))).sessions;
  }

  /** budget reads the caller's budget windows, or one user's (administrators only; "*" is refused). */
  budget(user: Scope = ""): Promise<BudgetStatus> {
    return this.get(scoped("/api/budget", user));
  }

  async keys(user: Scope = ""): Promise<ClientKey[]> {
    return (await this.get<{ keys: ClientKey[] }>(scoped("/api/keys", user))).keys;
  }

  /** decideDevice approves or denies an opencode device login by the code it shows. */
  async decideDevice(userCode: string, decision: "approve" | "deny"): Promise<void> {
    await this.call("POST", "/api/device", JSON.stringify({ user_code: userCode, decision }));
  }

  async revokeKey(id: number): Promise<void> {
    await this.call("DELETE", `/api/keys/${id}`);
  }

  /** revokeKeys revokes every active key of the caller, or of a user (administrators only), and returns the count. */
  async revokeKeys(user: Scope = ""): Promise<number> {
    return ((await (await this.call("POST", scoped("/api/keys/revoke", user))).json()) as { revoked: number })
      .revoked;
  }

  async revokeUser(email: string): Promise<void> {
    await this.call("POST", `/api/users/${encodeURIComponent(email)}/revoke`);
  }

  async logout(): Promise<void> {
    await this.call("POST", "/auth/logout");
  }
}

export const clientKey: InjectionKey<Client> = Symbol("client");
export const identityKey: InjectionKey<Ref<Identity | null>> = Symbol("identity");

/** useClient returns the API client the shell provides to its views and widgets. */
export function useClient(): Client {
  const client = inject(clientKey);
  if (!client) throw new Error("Client is not provided");
  return client;
}

/** useIdentity returns the signed-in caller; views render only after sign-in, so it is set there. */
export function useIdentity(): Ref<Identity | null> {
  const me = inject(identityKey);
  if (!me) throw new Error("Identity is not provided");
  return me;
}
