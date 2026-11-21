<script setup lang="ts">
// A timestamp cell: the absolute UTC time with the relative one ("5 min ago") muted beside it. Compact cells show
// "Nov 19, 22:51" for narrow columns and keep the full UTC time in the title.
import { compactTime, missing, relative, time } from "../format";
import { useNow } from "../now";

defineProps<{ iso: string | null; compact?: boolean }>();
const now = useNow();
</script>

<template>
  <span v-if="iso === null">{{ missing }}</span>
  <span v-else class="time" :title="compact ? time(iso) : iso">
    <span class="mono">{{ compact ? compactTime(iso) : time(iso) }}</span>
    <span class="muted rel">{{ relative(iso, now) }}</span>
  </span>
</template>

<style scoped>
.time {
  display: inline-flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 0 8px;
}
.rel {
  font-size: 12px;
}
</style>
