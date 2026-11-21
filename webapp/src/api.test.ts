import { describe, expect, it } from "vitest";
import { Client } from "./api";

// recorder answers every call with an empty list body and records the requested URLs.
function recorder() {
  const urls: string[] = [];
  const fn = (async (input: RequestInfo | URL) => {
    urls.push(String(input));
    const body = { attempts: [], sessions: [], keys: [], alerts: [], enabled: true, revoked: 3 };
    return new Response(JSON.stringify(body), { status: 200 });
  }) as typeof fetch;
  return { client: new Client(fn), urls };
}

const userOf = (url: string) => new URL(url, "http://x").searchParams.get("user");

describe("client scope", () => {
  it("leaves the user out by default, so the server answers for the caller", async () => {
    const { client, urls } = recorder();
    await Promise.all([client.sessions(50), client.keys(), client.usage("day"), client.alerts()]);
    expect(urls).toContain("/api/sessions?limit=50");
    expect(urls.map(userOf)).toEqual([null, null, null, null]);
  });

  it("passes everyone and a single user through", async () => {
    const { client, urls } = recorder();
    await Promise.all([
      client.sessions(50, "*"),
      client.keys("*"),
      client.usage("day", "", "a+b@example.com"),
      client.alerts("*"),
    ]);
    expect(urls.map(userOf)).toEqual(["*", "*", "a+b@example.com", "*"]);
  });

  it("reads the caller's status", async () => {
    const { client, urls } = recorder();
    await client.status();
    expect(urls).toEqual(["/api/status"]);
  });
});

describe("client user filters and bulk revoke", () => {
  it("filters sessions by status and scopes the budget", async () => {
    const { client, urls } = recorder();
    await client.sessions(50, "*", "active");
    await client.sessions(50, "", "offline");
    await client.budget();
    await client.budget("a+b@example.com");
    expect(urls).toEqual([
      "/api/sessions?limit=50&status=active&user=*",
      "/api/sessions?limit=50&status=offline",
      "/api/budget",
      "/api/budget?user=a%2Bb%40example.com",
    ]);
  });

  it("revokes all keys of the caller or of a user and returns the count", async () => {
    const calls: [string, string][] = [];
    const fn = (async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push([init?.method ?? "GET", String(input)]);
      return new Response(JSON.stringify({ revoked: 2 }), { status: 200 });
    }) as typeof fetch;
    const client = new Client(fn);
    expect(await client.revokeKeys()).toBe(2);
    expect(await client.revokeKeys("a@x")).toBe(2);
    expect(calls).toEqual([
      ["POST", "/api/keys/revoke"],
      ["POST", "/api/keys/revoke?user=a%40x"],
    ]);
  });
});
