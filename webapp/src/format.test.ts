import { describe, expect, it } from "vitest";
import {
  age,
  average,
  cents,
  price,
  compactTime,
  dollars,
  dollarsOf,
  nanos,
  relative,
  contextSize,
  resetsIn,
  share,
  usageOf,
} from "./format";

describe("money and share formatting", () => {
  it("parses and prints exact nano-dollar amounts", () => {
    expect(nanos("0.0004215")).toBe(421_500n);
    expect(nanos("12")).toBe(12_000_000_000n);
    expect(dollarsOf(421_500n)).toBe("$0.0004215");
    expect(dollarsOf(2_000_000_000n)).toBe("$2");
  });

  it("averages without floating point and shows missing data without a count", () => {
    expect(average("0.000003", 2)).toBe("$0.0000015");
    expect(average("0.000001", 3)).toBe("$0.000000333");
    expect(average("0", 0)).toBe("—");
  });

  it("has no share without a denominator", () => {
    expect(share(40, 100)).toBe("40%");
    expect(share(0, 0)).toBe("—");
  });

  it("renders budget cells", () => {
    const u = { limits: { day: "1" }, usage: [{ window: "day", spent: "0.5", percent: 50 }] };
    expect(usageOf(u, "day")).toBe("$0.5 of $1 (50%)");
    expect(usageOf({ limits: { week: "5" }, usage: [] }, "week")).toBe("limit $5");
  });

  it("renders decimal amounts in cents, with sub-cent spend marked and absent amounts missing", () => {
    expect(dollars("0.5")).toBe("$0.50");
    expect(dollars("1")).toBe("$1.00");
    expect(dollars("0.0589218")).toBe("$0.06");
    expect(dollars("0.0004")).toBe("<$0.01");
    expect(dollars("0.0099")).toBe("<$0.01");
    expect(dollars("0.01")).toBe("$0.01");
    expect(dollars("0")).toBe("$0.00");
    expect(dollars(null)).toBe("—");
    expect(dollars(undefined)).toBe("—");
  });

  it("renders a compact UTC time", () => {
    expect(compactTime("2026-11-19T22:51:07Z")).toBe("Nov 19, 22:51");
    expect(compactTime("2026-01-02T03:04:00+02:00")).toBe("Jan 2, 01:04");
  });

  it("rounds amounts to cents for at-a-glance figures", () => {
    expect(cents(nanos("195.8149"))).toBe("$195.81");
    expect(cents(nanos("7.966"))).toBe("$7.97");
    expect(cents(nanos("0.004"))).toBe("$0.00");
    expect(cents(nanos("0.005"))).toBe("$0.01");
    expect(cents(nanos("8"))).toBe("$8.00");
  });
});

describe("relative and elapsed time", () => {
  const now = Date.parse("2026-11-19T12:00:00Z");
  const at = (offsetMs: number) => new Date(now + offsetMs).toISOString();
  const min = 60_000;
  const hour = 60 * min;
  const day = 24 * hour;

  it.each([
    [-30_000, "just now"],
    [20_000, "just now"],
    [-5 * min, "5 min ago"],
    [-3 * hour, "3 h ago"],
    [-1 * day, "1 day ago"],
    [-2 * day, "2 days ago"],
    [3 * day, "in 3 days"],
    [4 * hour, "in 4 h"],
    [400 * day, "in 1 year"],
    [800 * day, "in 2 years"],
    [-400 * day, "1 year ago"],
    [Date.parse("2999-01-01T00:00:00Z") - now, "in over 100 years"],
    [-(100 * 365 * day), "over 100 years ago"],
  ])("renders %i ms from now as %s", (offset, want) => {
    expect(relative(at(offset), now)).toBe(want);
  });

  it.each([
    [42 * min, "42 min"],
    [3 * hour + 5 * min, "3 h 05 min"],
    [49 * hour, "2 days"],
    [25 * hour, "1 day"],
  ])("renders an elapsed %i ms as %s", (elapsed, want) => {
    expect(age(at(0), at(elapsed))).toBe(want);
  });
});

describe("price", () => {
  it.each([
    ["0.3", "$0.30"],
    ["1.2", "$1.20"],
    ["15", "$15.00"],
    ["0.075", "$0.075"],
    ["0.12500000", "$0.125"],
    [null, "\u2014"],
  ])("renders %s per 1M as %s", (amount, want) => {
    expect(price(amount)).toBe(want);
  });
});

describe("compact context and reset countdown", () => {
  it.each([
    [64_000, "64K"],
    [128_000, "128K"],
    [262_144, "262K"],
    [1_000_000, "1M"],
    [1_048_576, "1M"],
    [2_000_000, "2M"],
    [1_500_000, "1.5M"],
    [900, "900"],
  ])("renders %i tokens as %s", (n, want) => {
    expect(contextSize(n)).toBe(want);
  });

  it("renders a missing context as the placeholder", () => {
    expect(contextSize(null)).toBe("—");
  });

  const now = Date.parse("2026-11-20T00:30:00Z");
  it.each([
    ["2026-11-20T03:00:00Z", "in 2h 30m"],
    ["2026-11-20T00:45:00Z", "in 15m"],
    ["2026-11-23T04:30:00Z", "in 3d 4h"],
    ["2026-11-20T00:30:20Z", "in under a minute"],
    ["2026-11-20T00:00:00Z", "now"],
  ])("counts down to %s as %s", (iso, want) => {
    expect(resetsIn(iso, now)).toBe(want);
  });
});
