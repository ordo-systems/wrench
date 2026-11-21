<script setup lang="ts">
// A status as a colored pill with an icon; the label is the status itself. Unknown statuses render muted unless the
// caller sets the tone, which also picks the tone's icon (for computed labels such as "errors 3").
import { computed, type Component } from "vue";
import { CircleCheck, CircleMinus, CircleX, Clock, TriangleAlert } from "lucide-vue-next";

export type Tone = "ok" | "warn" | "bad" | "muted";

const looks: Record<string, [Tone, Component]> = {
  active: ["ok", CircleCheck],
  ok: ["ok", CircleCheck],
  offline: ["warn", Clock],
  pending: ["warn", Clock],
  warning: ["warn", TriangleAlert],
  critical: ["bad", CircleX],
  error: ["bad", CircleX],
  revoked: ["bad", CircleX],
  blocked: ["bad", CircleX],
  resolved: ["muted", CircleCheck],
  expired: ["muted", CircleMinus],
};

const toneIcons: Record<Tone, Component> = {
  ok: CircleCheck,
  warn: TriangleAlert,
  bad: CircleX,
  muted: CircleMinus,
};

const props = defineProps<{ status: string; tone?: Tone }>();
const look = computed(() =>
  props.tone
    ? ([props.tone, toneIcons[props.tone]] as const)
    : (looks[props.status] ?? (["muted", CircleMinus] as const)),
);
</script>

<template>
  <span class="pill status" :class="look[0]">
    <component :is="look[1]" :size="12" aria-hidden="true" />{{ status }}
  </span>
</template>

<style scoped>
.status {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  white-space: nowrap;
}
</style>
