import { inject, onBeforeUnmount, onMounted, ref, watch, type InjectionKey, type Ref } from "vue";
import { useClient, type Stats, type StatsQuery } from "./api";

// Live turns the server's event stream into debounced refetch callbacks. Events carry only a topic, so a
// handler refetches whatever it shows. The stream opens on the first listener, so signed-out pages and the
// device page never request it.
//
// The browser retries a dropped stream by itself, but gives up for good after an HTTP error (a proxy answers
// 502 while Wrench restarts). Live then opens a new stream with backoff, 1 s doubling up to 30 s, and tells
// onDropped listeners so the shell can check whether the session still exists.
// CLOSED is EventSource.CLOSED, spelled out so Live also runs where EventSource is not defined (tests).
const CLOSED = 2;
const firstDelayMs = 1000;
const maxDelayMs = 30_000;

export class Live {
  private source: EventSource | null = null;
  private handlers = new Map<string, Set<() => void>>();
  private timers = new Map<string, ReturnType<typeof setTimeout>>();
  private dropped = new Set<() => void>();
  private retry: ReturnType<typeof setTimeout> | null = null;
  private delay = firstDelayMs;
  private failed = false;

  constructor(
    private open: () => EventSource,
    private debounceMs = 1000,
  ) {}

  /** on registers fn for a topic and returns its unsubscribe function. */
  on(topic: string, fn: () => void): () => void {
    let set = this.handlers.get(topic);
    const added = !set;
    if (!set) {
      set = new Set();
      this.handlers.set(topic, set);
    }
    set.add(fn);
    // A pending reopen listens to every topic it finds, so a listener added during the backoff waits for it.
    if (!this.source) {
      if (!this.retry) this.connect();
    } else if (added) this.listen(this.source, topic);
    return () => void set.delete(fn);
  }

  /** onDropped registers fn for every time the browser closes the stream for good; it returns the unsubscribe. */
  onDropped(fn: () => void): () => void {
    this.dropped.add(fn);
    return () => void this.dropped.delete(fn);
  }

  close() {
    for (const t of this.timers.values()) clearTimeout(t);
    this.timers.clear();
    if (this.retry) clearTimeout(this.retry);
    this.retry = null;
    this.source?.close();
    this.source = null;
  }

  private connect() {
    const source = this.open();
    for (const topic of this.handlers.keys()) this.listen(source, topic);
    // Events sent while the stream was down are lost, so a reopen refetches everything.
    source.onerror = () => {
      this.failed = true;
      if (source.readyState === CLOSED) this.reopenLater(source);
    };
    source.onopen = () => {
      this.delay = firstDelayMs;
      if (!this.failed) return;
      this.failed = false;
      for (const set of this.handlers.values()) for (const fn of [...set]) fn();
    };
    this.source = source;
  }

  private listen(source: EventSource, topic: string) {
    source.addEventListener(topic, () => this.schedule(topic));
  }

  private reopenLater(source: EventSource) {
    if (this.source !== source) return;
    source.close();
    this.source = null;
    for (const fn of [...this.dropped]) fn();
    const wait = this.delay;
    this.delay = Math.min(this.delay * 2, maxDelayMs);
    // One reopen at a time: an orphaned timer would reopen the stream after close().
    if (this.retry) clearTimeout(this.retry);
    this.retry = setTimeout(() => {
      this.retry = null;
      if (!this.source) this.connect();
    }, wait);
  }

  // Events of a topic within one window collapse into one call at its end.
  private schedule(topic: string) {
    if (this.timers.has(topic)) return;
    this.timers.set(
      topic,
      setTimeout(() => {
        this.timers.delete(topic);
        for (const fn of [...(this.handlers.get(topic) ?? [])]) fn();
      }, this.debounceMs),
    );
  }
}

export const liveKey: InjectionKey<Live> = Symbol("live");

/** useLive returns the app's Live instance, provided by the shell. */
export function useLive(): Live {
  const live = inject(liveKey);
  if (!live) throw new Error("Live is not provided");
  return live;
}

/** useTopic loads a widget's data on mount and again on every event of its topics until it unmounts. */
export function useTopic(topics: string | string[], load: () => void | Promise<void>) {
  const live = useLive();
  let offs: (() => void)[] = [];
  onMounted(() => {
    offs = [topics].flat().map((t) => live.on(t, () => void load()));
    void load();
  });
  onBeforeUnmount(() => offs.forEach((off) => off()));
}

/**
 * useStats keeps chart aggregates for a query fresh: it loads on mount, on "usage" events and whenever the
 * query changes. Answers that arrive after a newer load started are dropped, so a slow answer for an old period
 * never replaces the current one.
 */
export function useStats(query: () => StatsQuery): { stats: Ref<Stats | null>; error: Ref<string> } {
  const client = useClient();
  const stats = ref<Stats | null>(null);
  const error = ref("");
  let gen = 0;
  async function load() {
    const mine = ++gen;
    try {
      const answer = (await client.stats(query())).stats;
      if (mine !== gen) return;
      stats.value = answer;
      error.value = "";
    } catch (e) {
      if (mine === gen) error.value = e instanceof Error ? e.message : String(e);
    }
  }
  useTopic("usage", load);
  watch(() => JSON.stringify(query()), load);
  return { stats, error };
}
