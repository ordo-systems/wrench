import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import ConfirmDialog from "./components/ConfirmDialog.vue";
import CopyButton from "./components/CopyButton.vue";
import Logo from "./components/Logo.vue";
import StatusPill from "./components/StatusPill.vue";
import TimeCell from "./components/TimeCell.vue";
import UserSelect from "./components/UserSelect.vue";

// jsdom has <dialog> without showModal and close; these stand-ins toggle the open attribute like a browser.
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

// confirmDialog mounts an open dialog bound like v-model:open, so closing it updates the prop.
function confirmDialog() {
  const wrapper = mount(ConfirmDialog, {
    props: {
      open: true,
      title: "Revoke key",
      message: "Revoke the key of laptop?",
      confirmLabel: "Revoke",
      "onUpdate:open": (v: boolean) => wrapper.setProps({ open: v }),
    },
  });
  return wrapper;
}

const isOpen = (w: ReturnType<typeof mount>) => w.find("dialog").element.hasAttribute("open");

describe("confirmation dialog", () => {
  let restore: () => void;
  beforeEach(() => (restore = stubDialog()));
  afterEach(() => restore());

  it("opens as a modal with the title, message and confirm label", () => {
    const w = confirmDialog();
    expect(isOpen(w)).toBe(true);
    expect(w.text()).toContain("Revoke key");
    expect(w.text()).toContain("Revoke the key of laptop?");
    expect(w.find("button.danger").text()).toBe("Revoke");
  });

  it("closes on Cancel without confirming", async () => {
    const w = confirmDialog();
    await w.find("button[data-cancel]").trigger("click");
    expect(w.emitted("confirm")).toBeUndefined();
    expect(w.emitted("update:open")?.at(-1)).toEqual([false]);
    expect(isOpen(w)).toBe(false);
  });

  it("closes on Escape without confirming", async () => {
    const w = confirmDialog();
    await w.find("dialog").trigger("cancel");
    expect(w.emitted("confirm")).toBeUndefined();
    expect(w.emitted("update:open")?.at(-1)).toEqual([false]);
    expect(isOpen(w)).toBe(false);
  });

  it("closes on a backdrop click without confirming", async () => {
    const w = confirmDialog();
    await w.find("dialog").trigger("click");
    expect(w.emitted("confirm")).toBeUndefined();
    expect(isOpen(w)).toBe(false);
  });

  it("emits confirm exactly once and closes", async () => {
    const w = confirmDialog();
    await w.find("button.danger").trigger("click");
    expect(w.emitted("confirm")).toHaveLength(1);
    expect(isOpen(w)).toBe(false);
  });

  it("does not open while open is false", () => {
    const w = mount(ConfirmDialog, { props: { open: false, title: "t", message: "m", confirmLabel: "c" } });
    expect(isOpen(w)).toBe(false);
  });
});

describe("status pill", () => {
  it.each([
    ["active", "ok"],
    ["ok", "ok"],
    ["offline", "warn"],
    ["pending", "warn"],
    ["warning", "warn"],
    ["critical", "bad"],
    ["error", "bad"],
    ["revoked", "bad"],
    ["blocked", "bad"],
    ["resolved", "muted"],
    ["expired", "muted"],
  ])("renders %s as %s with an icon and the label", (status, tone) => {
    const w = mount(StatusPill, { props: { status } });
    const pill = w.find(".pill");
    expect(pill.classes()).toContain(tone);
    expect(pill.find("svg").exists()).toBe(true);
    expect(pill.text()).toBe(status);
  });
});

describe("user select", () => {
  it("offers me, all users, then every email", async () => {
    const w = mount(UserSelect, {
      props: { modelValue: "", emails: ["b@x", "c@x"], me: "a@x" },
    });
    const options = w.findAll("option");
    expect(options.map((o) => o.text())).toEqual(["Me (a@x)", "All users", "b@x", "c@x"]);
    expect(options.map((o) => o.element.value)).toEqual(["", "*", "b@x", "c@x"]);
    await w.find("select").setValue("*");
    expect(w.emitted("update:modelValue")).toEqual([["*"]]);
  });
});

describe("time cell", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(Date.parse("2026-11-19T12:00:00Z"));
  });
  afterEach(() => vi.useRealTimers());

  it("shows the absolute time and a relative one that ticks every minute", async () => {
    const w = mount(TimeCell, { props: { iso: "2026-11-19T11:55:00Z" } });
    expect(w.text()).toContain("2026-11-19 11:55:00 UTC");
    expect(w.text()).toContain("5 min ago");
    vi.advanceTimersByTime(60_000);
    await w.vm.$nextTick();
    expect(w.text()).toContain("6 min ago");
    w.unmount();
  });

  it("shows the placeholder without a time", () => {
    const w = mount(TimeCell, { props: { iso: null } });
    expect(w.text()).toBe("—");
  });
});

describe("logo", () => {
  it("draws in the current color at the given size", () => {
    const svg = mount(Logo, { props: { size: 16 } }).find("svg");
    expect(svg.attributes("width")).toBe("16");
    expect(svg.html()).toContain("currentColor");
  });
});

describe("copy button", () => {
  const setClipboard = (value: unknown) =>
    Object.defineProperty(navigator, "clipboard", { value, configurable: true });
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    vi.useRealTimers();
    setClipboard(undefined);
  });

  it("shows Copied with a check icon for 2 s after a successful copy, then returns to idle", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    setClipboard({ writeText });
    const w = mount(CopyButton, { props: { text: "abc", label: "Copy command" } });
    const button = w.find("button");
    expect(button.text()).toBe("Copy command");
    expect(button.attributes("data-state")).toBe("idle");
    await button.trigger("click");
    await flushPromises();
    expect(writeText).toHaveBeenCalledWith("abc");
    expect(button.text()).toBe("Copied");
    expect(button.attributes("data-state")).toBe("copied");
    expect(button.find("svg.lucide-check-icon").exists()).toBe(true);
    vi.advanceTimersByTime(1999);
    await flushPromises();
    expect(button.text()).toBe("Copied");
    vi.advanceTimersByTime(1);
    await flushPromises();
    expect(button.text()).toBe("Copy command");
    expect(button.find("svg.lucide-copy-icon").exists()).toBe(true);
  });

  it("shows Copy failed when the clipboard rejects", async () => {
    setClipboard({ writeText: vi.fn().mockRejectedValue(new Error("denied")) });
    const w = mount(CopyButton, { props: { text: "abc" } });
    await w.find("button").trigger("click");
    await flushPromises();
    expect(w.find("button").text()).toBe("Copy failed");
    expect(w.find("button").attributes("data-state")).toBe("failed");
    // lucide names the X icon's class "lucide-xicon" (no dash before a capital that follows a capital).
    expect(w.find("button svg.lucide-xicon").exists()).toBe(true);
    vi.advanceTimersByTime(2000);
    await flushPromises();
    expect(w.find("button").text()).toBe("Copy");
  });

  it("shows Copy failed without a clipboard and does not throw", async () => {
    setClipboard(undefined);
    const w = mount(CopyButton, { props: { text: "abc" } });
    await w.find("button").trigger("click");
    await flushPromises();
    expect(w.find("button").text()).toBe("Copy failed");
  });

  it("keeps an icon-only button empty and names its state in the accessible name", async () => {
    setClipboard({ writeText: vi.fn().mockResolvedValue(undefined) });
    const w = mount(CopyButton, { props: { text: "abc", label: "Copy session id", iconOnly: true } });
    const button = w.find("button");
    expect(button.text()).toBe("");
    expect(button.attributes("aria-label")).toBe("Copy session id");
    await button.trigger("click");
    await flushPromises();
    expect(button.text()).toBe("");
    expect(button.attributes("aria-label")).toBe("Copied");
    expect(button.attributes("title")).toBe("Copied");
    expect(w.find("[role='status']").text()).toBe("Copied");
  });
});
