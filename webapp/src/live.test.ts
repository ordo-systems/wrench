import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Live } from "./live";

// FakeSource is the part of EventSource that Live uses.
class FakeSource {
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  readyState = 1;
  closed = false;
  private listeners = new Map<string, (() => void)[]>();
  addEventListener(topic: string, fn: () => void) {
    this.listeners.set(topic, [...(this.listeners.get(topic) ?? []), fn]);
  }
  emit(topic: string) {
    for (const fn of this.listeners.get(topic) ?? []) fn();
  }
  close() {
    this.closed = true;
    this.readyState = 2;
  }
}

// sources opens a new FakeSource per connection, like the browser does after a reconnect.
function sources() {
  const all: FakeSource[] = [];
  const live = new Live(() => {
    const s = new FakeSource();
    all.push(s);
    return s as unknown as EventSource;
  }, 1000);
  return { all, live, last: () => all[all.length - 1] as FakeSource };
}

// fail makes the browser give up on a source, as it does after an HTTP error such as a 502 from a proxy.
function fail(s: FakeSource) {
  s.readyState = 2;
  s.onerror?.();
}

function setup() {
  const source = new FakeSource();
  const live = new Live(() => source as unknown as EventSource, 1000);
  return { source, live };
}

describe("live updates", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("calls a handler once for a burst of events within a second", () => {
    const { source, live } = setup();
    const fn = vi.fn();
    live.on("usage", fn);
    source.emit("usage");
    source.emit("usage");
    source.emit("usage");
    vi.advanceTimersByTime(1000);
    expect(fn).toHaveBeenCalledTimes(1);
    source.emit("usage");
    vi.advanceTimersByTime(1000);
    expect(fn).toHaveBeenCalledTimes(2);
  });

  it("keeps topics independent", () => {
    const { source, live } = setup();
    const usage = vi.fn();
    const keys = vi.fn();
    live.on("usage", usage);
    live.on("keys", keys);
    source.emit("keys");
    vi.advanceTimersByTime(1000);
    expect(usage).not.toHaveBeenCalled();
    expect(keys).toHaveBeenCalledTimes(1);
  });

  it("stops calling an unsubscribed handler", () => {
    const { source, live } = setup();
    const fn = vi.fn();
    const off = live.on("usage", fn);
    off();
    source.emit("usage");
    vi.advanceTimersByTime(1000);
    expect(fn).not.toHaveBeenCalled();
  });

  it("refetches everything once when the stream reopens after an error", () => {
    const { source, live } = setup();
    const usage = vi.fn();
    const status = vi.fn();
    live.on("usage", usage);
    live.on("status", status);
    source.onopen?.();
    expect(usage).not.toHaveBeenCalled();
    source.onerror?.();
    source.onopen?.();
    expect(usage).toHaveBeenCalledTimes(1);
    expect(status).toHaveBeenCalledTimes(1);
    source.onopen?.();
    expect(usage).toHaveBeenCalledTimes(1);
  });

  it("closes the stream and drops pending calls", () => {
    const { source, live } = setup();
    const fn = vi.fn();
    live.on("usage", fn);
    source.emit("usage");
    live.close();
    vi.advanceTimersByTime(1000);
    expect(source.closed).toBe(true);
    expect(fn).not.toHaveBeenCalled();
  });

  it("does not open a stream until something listens", () => {
    const open = vi.fn();
    new Live(open, 1000);
    expect(open).not.toHaveBeenCalled();
  });

  it("reopens a stream the browser closed and refetches every topic", () => {
    const { all, live, last } = sources();
    const usage = vi.fn();
    const status = vi.fn();
    const dropped = vi.fn();
    live.onDropped(dropped);
    live.on("usage", usage);
    live.on("status", status);
    fail(last());
    expect(all[0]?.closed).toBe(true);
    expect(dropped).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(999);
    expect(all).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(all).toHaveLength(2);
    last().onopen?.();
    expect(usage).toHaveBeenCalledTimes(1);
    expect(status).toHaveBeenCalledTimes(1);
    last().emit("usage");
    vi.advanceTimersByTime(1000);
    expect(usage).toHaveBeenCalledTimes(2);
  });

  it("backs off between reopen attempts up to 30 seconds and resets after an open", () => {
    const { all, last } = (() => {
      const r = sources();
      r.live.on("usage", () => {});
      return r;
    })();
    const delays: number[] = [];
    for (let i = 0; i < 7; i++) {
      const before = all.length;
      fail(last());
      let waited = 0;
      while (all.length === before && waited < 60_000) {
        vi.advanceTimersByTime(1000);
        waited += 1000;
      }
      delays.push(waited);
    }
    expect(delays).toEqual([1000, 2000, 4000, 8000, 16000, 30000, 30000]);
    last().onopen?.();
    fail(last());
    const before = all.length;
    vi.advanceTimersByTime(1000);
    expect(all.length).toBe(before + 1);
  });

  it("does not reopen after close", () => {
    const { all, live, last } = sources();
    live.on("usage", () => {});
    fail(last());
    live.close();
    vi.advanceTimersByTime(60_000);
    expect(all).toHaveLength(1);
  });

  it("on() during backoff, then fail, then close(): no source opens", () => {
    const { all, live, last } = sources();
    live.on("usage", () => {});
    fail(last());
    // A new listener while the reopen is pending waits for that reopen instead of connecting now.
    live.on("status", () => {});
    expect(all).toHaveLength(1);
    vi.advanceTimersByTime(1000);
    expect(all).toHaveLength(2);
    fail(last());
    live.close();
    vi.advanceTimersByTime(60_000);
    expect(all).toHaveLength(2);
  });

  it("listens to a topic added during backoff once the stream reopens", () => {
    const { live, last } = sources();
    live.on("usage", () => {});
    fail(last());
    const status = vi.fn();
    live.on("status", status);
    vi.advanceTimersByTime(1000);
    last().emit("status");
    vi.advanceTimersByTime(1000);
    expect(status).toHaveBeenCalledTimes(1);
  });

  it("delivers events to a topic re-subscribed after close()", () => {
    const { live, last } = sources();
    const first = vi.fn();
    const second = vi.fn();
    const off = live.on("usage", first);
    off();
    live.close();
    live.on("usage", second);
    last().emit("usage");
    vi.advanceTimersByTime(1000);
    expect(second).toHaveBeenCalledTimes(1);
    expect(first).not.toHaveBeenCalled();
  });
});
