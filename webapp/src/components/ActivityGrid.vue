<script setup lang="ts">
// ActivityGrid shows a usage card's buckets as one line of cells (layout in metrics.activityLayout).
// Cells step through four shades of the accent over the period's maximum; no data and zero use the line color,
// so the grid reads in both themes. Each cell's title names its time and value; the grid needs no chart library.
import { computed } from "vue";
import type { Period } from "../api";
import { activityLayout, type ActivityBucket } from "../metrics";

const props = defineProps<{
  buckets: ActivityBucket[];
  period: Period;
  format?: (v: number) => string;
}>();

const layout = computed(() => activityLayout(props.buckets, props.period, props.format));
</script>

<template>
  <div class="activity" :class="`p${period}`" :style="{ '--cols': layout.cols, '--rows': layout.rows }">
    <span
      v-for="(c, i) in layout.cells"
      :key="i"
      class="cell"
      :class="`l${c.level}`"
      :style="{ gridRow: c.row + 1, gridColumn: c.col + 1 }"
      :title="c.title"
      :data-row="c.row"
      :data-col="c.col"
      :data-level="c.level"
      :data-blank="c.blank || undefined"
      data-cell
    />
  </div>
</template>

<style scoped>
.activity {
  display: grid;
  gap: 2px;
  grid-template-columns: repeat(var(--cols), minmax(0, 1fr));
}
.cell {
  height: 16px;
  border-radius: 2px;
  background: var(--line);
}
/* 168 hourly cells are under 2 px wide in a card: drawn edge to edge they read as one continuous heat strip. */
.p7d {
  gap: 0;
}
.p7d .cell {
  border-radius: 0;
}
.cell[data-blank] {
  visibility: hidden;
}
.l1 {
  background: color-mix(in srgb, var(--accent) 30%, var(--line));
}
.l2 {
  background: color-mix(in srgb, var(--accent) 55%, var(--line));
}
.l3 {
  background: color-mix(in srgb, var(--accent) 80%, var(--line));
}
.l4 {
  background: var(--accent);
}
</style>
