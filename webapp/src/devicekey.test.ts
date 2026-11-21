import src from "./components/DeviceKeyHelp.vue?raw";
import { describe, expect, it } from "vitest";

describe("device key command", () => {
  // jsdom does not apply scoped styles, so the wrapping rule is checked in the component source.
  it("stays on one line and scrolls horizontally instead of wrapping inside a token", () => {
    const style = src.slice(src.indexOf("<style"));
    expect(style).toMatch(/white-space:\s*pre;/);
    expect(style).toMatch(/overflow-x:\s*auto;/);
    expect(style).not.toMatch(/overflow-wrap:\s*anywhere/);
  });
});
