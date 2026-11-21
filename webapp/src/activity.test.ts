import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import ActivityGrid from "./components/ActivityGrid.vue";

const hour = 3_600_000;
const day = 24 * hour;

// series makes count consecutive buckets of the given width ending with the bucket that starts at last.
function series(last: string, count: number, width: number, value: (i: number) => number | null = (i) => i) {
  const end = Date.parse(last);
  return Array.from({ length: count }, (_, i) => ({
    at: new Date(end - (count - 1 - i) * width).toISOString().replace(".000", ""),
    value: value(i),
  }));
}

function grid(buckets: { at: string; value: number | null }[], period: "24h" | "7d" | "30d") {
  const w = mount(ActivityGrid, { props: { buckets, period } });
  const cells = w.findAll("[data-cell]").map((c) => ({
    row: Number(c.attributes("data-row")),
    col: Number(c.attributes("data-col")),
    blank: c.attributes("data-blank") !== undefined,
    level: Number(c.attributes("data-level")),
    title: c.attributes("title"),
  }));
  return { w, cells };
}

describe("activity grid", () => {
  it("lays out 24 hourly cells in one row for 24 hours", () => {
    // The 24-hour period carries 25 buckets (the window starts on an hour boundary); the grid shows the last 24.
    const { cells } = grid(series("2026-11-09T10:00:00Z", 25, hour), "24h");
    expect(cells).toHaveLength(24);
    expect(new Set(cells.map((c) => c.row))).toEqual(new Set([0]));
    expect(cells.map((c) => c.col)).toEqual(Array.from({ length: 24 }, (_, i) => i));
    expect(cells[0]?.title).toBe("2026-11-08 11:00 UTC: 1");
    expect(cells[23]?.title).toBe("2026-11-09 10:00 UTC: 24");
    expect(cells[23]?.level).toBe(4);
  });

  it("lays out the last 168 hours in one row for 7 days, ending with the last bucket", () => {
    const { cells } = grid(series("2026-11-09T10:00:00Z", 169, hour), "7d");
    expect(cells).toHaveLength(168);
    expect(new Set(cells.map((c) => c.row))).toEqual(new Set([0]));
    expect(cells.map((c) => c.col)).toEqual(Array.from({ length: 168 }, (_, i) => i));
    expect(cells.some((c) => c.blank)).toBe(false);
    expect(cells[0]?.title).toBe("2026-11-02 11:00 UTC: 1");
    expect(cells[167]?.title).toBe("2026-11-09 10:00 UTC: 168");
  });

  it("lays out the last 30 days in one row for 30 days", () => {
    const { cells } = grid(series("2026-11-10T00:00:00Z", 31, day), "30d");
    expect(cells).toHaveLength(30);
    expect(new Set(cells.map((c) => c.row))).toEqual(new Set([0]));
    expect(cells.some((c) => c.blank)).toBe(false);
    expect(cells[0]).toMatchObject({ col: 0, title: "2026-10-12: 1" });
    expect(cells[29]).toMatchObject({ col: 29, title: "2026-11-10: 30" });
  });

  it("titles unreported and missing buckets as no data at the lowest level", () => {
    const buckets = series("2026-11-09T10:00:00Z", 2, hour, (i) => (i === 0 ? null : 4));
    const { cells } = grid(buckets, "24h");
    expect(cells[22]).toMatchObject({ level: 0, title: "2026-11-09 09:00 UTC: no data" });
    expect(cells[0]).toMatchObject({ level: 0, title: "2026-11-08 11:00 UTC: no data" });
    expect(cells[23]).toMatchObject({ level: 4, title: "2026-11-09 10:00 UTC: 4" });
  });

  it("formats values with the given formatter", () => {
    const w = mount(ActivityGrid, {
      props: {
        buckets: series("2026-11-09T10:00:00Z", 1, hour, () => 0.4),
        period: "24h",
        format: (v: number) => `$${v}`,
      },
    });
    expect(w.findAll("[data-cell]").at(-1)?.attributes("title")).toBe("2026-11-09 10:00 UTC: $0.4");
  });
});
