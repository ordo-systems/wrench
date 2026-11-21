<script setup lang="ts">
// CopyButton copies `text` to the clipboard and shows the outcome for 2 s: a check and "Copied", or an X and
// "Copy failed" (a rejected write or no clipboard API, as on plain HTTP), then returns to idle. An icon-only
// button keeps its text empty and names its state through aria-label, title and a hidden live region.
import { computed, onBeforeUnmount, ref } from "vue";
import { Check, Copy, X } from "lucide-vue-next";

defineOptions({ inheritAttrs: false });

const props = withDefaults(defineProps<{ text: string; label?: string; iconOnly?: boolean }>(), {
  label: "Copy",
  iconOnly: false,
});

type State = "idle" | "copied" | "failed";
const resetMs = 2000;

const state = ref<State>("idle");
let timer: ReturnType<typeof setTimeout> | null = null;

const looks = { idle: Copy, copied: Check, failed: X } as const;
const icon = computed(() => looks[state.value]);
const caption = computed(() =>
  state.value === "copied" ? "Copied" : state.value === "failed" ? "Copy failed" : props.label,
);

async function write(): Promise<boolean> {
  try {
    if (!navigator.clipboard) return false;
    await navigator.clipboard.writeText(props.text);
    return true;
  } catch {
    return false;
  }
}

async function copy() {
  const done = await write();
  state.value = done ? "copied" : "failed";
  if (timer) clearTimeout(timer);
  timer = setTimeout(() => {
    timer = null;
    state.value = "idle";
  }, resetMs);
}

onBeforeUnmount(() => {
  if (timer) clearTimeout(timer);
});
</script>

<template>
  <span class="copy-button">
    <button
      v-bind="$attrs"
      type="button"
      :class="{ icon: iconOnly }"
      :data-state="state"
      :aria-label="iconOnly ? caption : undefined"
      :title="iconOnly ? caption : undefined"
      @click="copy"
    >
      <component :is="icon" :size="14" aria-hidden="true" /><template v-if="!iconOnly">{{
        caption
      }}</template>
    </button>
    <span v-if="iconOnly" class="visually-hidden" role="status">{{ state === "idle" ? "" : caption }}</span>
  </span>
</template>

<style scoped>
.copy-button {
  display: inline-flex;
}
[data-state="copied"] {
  border-color: var(--ok);
  color: var(--ok);
}
[data-state="failed"] {
  border-color: var(--bad);
  color: var(--bad);
}
</style>
