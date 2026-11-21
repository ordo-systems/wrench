import { describe, expect, it } from "vitest";
import { allowed, landing, routes } from "./router";

describe("routing", () => {
  it("lands each role on its first tab", () => {
    expect(landing("admin")).toBe("/overview");
    expect(landing("user")).toBe("/usage");
  });

  it.each([
    ["/users", "user", false],
    ["/overview", "user", false],
    ["/alerts", "user", false],
    ["/usage/spend", "user", true],
    ["/overview/spend", "user", false],
    ["/sessions", "user", true],
    ["/keys", "user", true],
    ["/device", "user", true],
    ["/users", "admin", true],
    ["/overview/spend", "admin", true],
    ["/usage", "admin", true],
  ] as const)("allowed(%s, %s) is %s", (path, role, want) => {
    expect(allowed(path, role)).toBe(want);
  });

  it("declares every spec route, with /device outside the shell", () => {
    const paths = routes.map((r) => r.path);
    for (const p of ["/overview", "/overview/:metric", "/usage", "/usage/:metric", "/sessions", "/keys"])
      expect(paths).toContain(p);
    for (const p of ["/users", "/alerts", "/device"]) expect(paths).toContain(p);
    expect(paths).not.toContain("/requests");
    expect(routes.find((r) => r.path === "/device")?.meta?.bare).toBe(true);
  });
});
