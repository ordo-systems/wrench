<script setup lang="ts">
// BudgetWidget shows one bar per budget window of a scope: the caller's own windows ("user") or the shared
// company windows ("global", returned to administrators only). An administrator may pass user to see one user's
// windows instead of their own. Marks sit at the configured warning thresholds; an hourglass flags a window with
// finished requests whose cost is still unknown.
// Config reloads publish "status"; a window reset publishes nothing, so the widget refetches when the earliest
// window resets.
import { computed, onBeforeUnmount, ref, watch } from "vue";
import UnsettledMark from "../components/UnsettledMark.vue";
import { useClient, type BudgetStatus, type BudgetWindow, type Scope } from "../api";
import * as fmt from "../format";
import { useTopic } from "../live";
import { useNow } from "../now";

const props = defineProps<{ scope: "user" | "global"; user?: Scope }>();
// now ticks once a minute, so the reset countdowns stay current without a refetch.
const now = useNow();

const client = useClient();
const budget = ref<BudgetStatus | null>(null);
const error = ref("");

let resetTimer: ReturnType<typeof setTimeout> | undefined;
let unmounted = false;
// gen drops an answer for a user that is no longer selected.
let gen = 0;

// setTimeout overflows above this delay and would fire at once; a later refetch reschedules.
const maxDelayMs = 2 ** 31 - 1;

async function load() {
  const mine = ++gen;
  try {
    const answer = await client.budget(props.user ?? "");
    if (mine !== gen) return;
    budget.value = answer;
    error.value = "";
    scheduleReset();
  } catch (e) {
    if (mine === gen) error.value = e instanceof Error ? e.message : String(e);
  }
}

// scheduleReset refetches one second after the earliest future reset, when the server reports the new window.
function scheduleReset() {
  clearTimeout(resetTimer);
  if (unmounted) return;
  const now = Date.now();
  const next = (budget.value?.windows ?? [])
    .map((w) => Date.parse(w.resets_at))
    .filter((t) => t > now)
    .sort((a, b) => a - b)[0];
  if (next === undefined) return;
  resetTimer = setTimeout(() => void load(), Math.min(next - now + 1000, maxDelayMs));
}

useTopic(["budget", "status"], load);
watch(
  () => props.user,
  () => void load(),
);
onBeforeUnmount(() => {
  unmounted = true;
  clearTimeout(resetTimer);
});

const order = ["5_hours", "day", "week", "month"];
const windows = computed(() =>
  (budget.value?.windows ?? [])
    .filter((w) => w.scope === props.scope)
    .sort((a, b) => order.indexOf(a.window) - order.indexOf(b.window)),
);

const title = computed(() => {
  if (props.scope === "global") return "Shared budget";
  return props.user ? `Budget of ${props.user}` : "Your budget";
});

function remaining(w: BudgetWindow): string {
  const left = fmt.nanos(w.limit) - fmt.nanos(w.spent);
  return fmt.cents(left > 0n ? left : 0n);
}

function tone(w: BudgetWindow): string {
  if (w.percent >= 100) return "bad";
  return w.threshold !== undefined ? "warn" : "ok";
}
</script>

<template>
  <section class="card">
    <h2>{{ title }}</h2>
    <p v-if="error" class="error">{{ error }}</p>
    <p v-else-if="budget && !windows.length" class="muted">No budget windows are configured.</p>
    <div v-for="w in windows" :key="w.window" class="window" :data-window="w.window">
      <div class="head">
        <strong>{{ fmt.windowName(w.window) }}<UnsettledMark :count="w.unsettled" /></strong>
        <span class="mono">{{ remaining(w) }} left</span>
      </div>
      <div class="track" role="meter" :aria-valuenow="w.percent" aria-valuemin="0" aria-valuemax="100">
        <div class="fill" :class="tone(w)" :style="{ width: `${Math.min(w.percent, 100)}%` }"></div>
        <span
          v-for="p in budget?.warn_at_percent ?? []"
          :key="p"
          class="mark"
          data-mark
          :style="{ left: `${p}%` }"
        ></span>
      </div>
      <div class="foot muted">
        <span class="mono"
          >{{ fmt.cents(fmt.nanos(w.spent)) }} of {{ fmt.cents(fmt.nanos(w.limit)) }} ({{ w.percent }}%)</span
        >
        <span data-resets :title="fmt.time(w.resets_at)">resets {{ fmt.resetsIn(w.resets_at, now) }}</span>
      </div>
    </div>
  </section>
</template>

<style scoped>
.window + .window {
  margin-top: 14px;
}
.head,
.foot {
  display: flex;
  justify-content: space-between;
  gap: 8px;
  font-size: 14px;
}
.foot {
  font-size: 12px;
  margin-top: 4px;
}
.track {
  position: relative;
  height: 10px;
  margin-top: 6px;
  border: 1px solid var(--line);
  border-radius: 999px;
  overflow: hidden;
}
.fill {
  height: 100%;
  background: var(--ok);
}
.fill.warn {
  background: var(--warn);
}
.fill.bad {
  background: var(--bad);
}
.mark {
  position: absolute;
  top: 0;
  bottom: 0;
  width: 2px;
  background: var(--text);
  opacity: 0.6;
}
</style>
