<script setup lang="ts">
// Chart draws metrics.ts series with ECharts. Only the chart types and components the dashboard uses are
// registered, so the bundle stays small. Colors come from the theme tokens and follow the theme toggle.
import { computed } from "vue";
import VChart from "vue-echarts";
import { use } from "echarts/core";
import { BarChart, LineChart } from "echarts/charts";
import { DataZoomComponent, GridComponent, LegendComponent, TooltipComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import { chartOption, type Series } from "../metrics";
import { theme } from "../theme";

use([
  LineChart,
  BarChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
  DataZoomComponent,
  CanvasRenderer,
]);

const props = defineProps<{
  label: string;
  starts: string[];
  series: Series[];
  kind: "line" | "bar";
  format?: (v: number) => string;
}>();

const option = computed(() => {
  // Reading the theme makes the option recompute after a toggle, when the tokens have new values.
  void theme.value;
  const style = getComputedStyle(document.documentElement);
  return chartOption(props, (token) => style.getPropertyValue(token).trim() || "#888");
});
</script>

<template>
  <div class="chart" role="img" :aria-label="label">
    <VChart :option="option" autoresize />
  </div>
</template>

<style scoped>
.chart {
  height: 320px;
}
</style>
