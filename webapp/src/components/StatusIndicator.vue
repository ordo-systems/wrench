<script setup lang="ts">
// StatusIndicator is the top bar's icon answer to "do requests run?": Running (inference runs and, for
// administrators, every system check is ok), Degraded (runs, but a check is not ok), Paused, or Unknown before
// the first answer and after a failed one. Clicking it opens a popover with the pause reasons and, for
// administrators, the checks. It refetches on "status" events.
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useClient, type Status, type SystemInfo } from "../api";
import * as fmt from "../format";
import { useTopic } from "../live";
import { CircleCheck, CircleHelp, CircleX, TriangleAlert } from "lucide-vue-next";
import type { Tone } from "./StatusPill.vue";

const icons: Record<Tone, unknown> = {
  ok: CircleCheck,
  warn: TriangleAlert,
  bad: CircleX,
  muted: CircleHelp,
};

const props = defineProps<{ admin: boolean }>();

const client = useClient();
const status = ref<Status | null>(null);
const system = ref<SystemInfo | null>(null);
const error = ref("");
const open = ref(false);
const root = ref<HTMLElement | null>(null);

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

// A failed status or system answer drops the old one: a stale "Running" is worse than "Unknown", and an
// administrator without the checks cannot tell Running from Degraded.
useTopic("status", async () => {
  const [s, sys] = await Promise.allSettled([client.status(), props.admin ? client.system() : null]);
  if (s.status === "fulfilled") {
    status.value = s.value;
    error.value = "";
  } else {
    status.value = null;
    error.value = message(s.reason);
  }
  if (!props.admin) return;
  if (sys.status === "fulfilled" && sys.value) {
    system.value = sys.value;
  } else {
    system.value = null;
    if (sys.status === "rejected" && !error.value) error.value = message(sys.reason);
  }
});

const failedChecks = computed(() => Object.entries(system.value?.checks ?? {}).filter(([, v]) => v !== "ok"));

const look = computed<[string, Tone]>(() => {
  if (!status.value || (props.admin && !system.value)) return ["Unknown", "muted"];
  if (status.value.inference.state !== "ok") return ["Paused", "bad"];
  if (failedChecks.value.length) return ["Degraded", "warn"];
  return ["Running", "ok"];
});

// The popover closes on Escape and on a click outside the indicator.
function onDocumentClick(e: MouseEvent) {
  if (open.value && root.value && !root.value.contains(e.target as Node)) open.value = false;
}
onMounted(() => document.addEventListener("click", onDocumentClick));
onBeforeUnmount(() => document.removeEventListener("click", onDocumentClick));
</script>

<template>
  <span ref="root" class="indicator" @keydown.esc="open = false">
    <button
      type="button"
      class="pill"
      :class="look[1]"
      data-status-indicator
      aria-haspopup="dialog"
      :aria-expanded="open ? 'true' : 'false'"
      aria-controls="status-popover"
      :title="look[0]"
      :aria-label="`Status: ${look[0]}`"
      @click="open = !open"
    >
      <component :is="icons[look[1]]" :size="18" aria-hidden="true" />
    </button>
    <div
      v-if="open"
      id="status-popover"
      class="popover card"
      role="dialog"
      aria-label="Status"
      data-status-popover
    >
      <p v-if="error" class="error">{{ error }}</p>
      <p v-else-if="!status" class="muted">Waiting for the status.</p>
      <template v-else>
        <p v-if="status.inference.state === 'ok'">Requests run.</p>
        <template v-else>
          <p>Requests are paused:</p>
          <ul class="reasons">
            <li v-for="r in status.inference.reasons" :key="r.code">{{ fmt.pauseReason(r) }}</li>
          </ul>
        </template>
      </template>
      <table v-if="admin && system" data-checks>
        <tbody>
          <tr v-for="(v, k) in system.checks" :key="k">
            <th>{{ k }}</th>
            <td>
              <span class="pill" :class="v === 'ok' ? 'ok' : 'bad'">{{ v }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </span>
</template>

<style scoped>
.indicator {
  position: relative;
  display: inline-flex;
}
/* An icon-only button: the tone classes from the shared .pill styles color the icon; border and padding go. */
.indicator button.pill {
  display: inline-flex;
  padding: 4px;
  border: 0;
  border-radius: 50%;
  background: transparent;
}
.popover {
  position: absolute;
  top: calc(100% + 8px);
  right: 0;
  z-index: 10;
  width: max-content;
  max-width: min(360px, calc(100vw - 32px));
  font-size: 14px;
  box-shadow: 0 8px 24px rgb(0 0 0 / 25%);
}
.popover p {
  margin: 0 0 8px;
  white-space: normal;
}
.reasons {
  margin: 0 0 8px;
  padding-left: 18px;
}
</style>
