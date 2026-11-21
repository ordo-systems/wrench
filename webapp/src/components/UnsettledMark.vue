<script setup lang="ts">
// UnsettledMark flags a budget window whose spend may be understated: finished requests there have a cost
// OpenRouter has not confirmed yet, so it is not counted. It renders nothing for zero.
import { Hourglass } from "lucide-vue-next";
import { computed } from "vue";

const props = defineProps<{ count: number }>();
const label = computed(() => `${props.count} finished request(s) with an unknown cost; not counted yet`);
</script>

<template>
  <span v-if="count > 0" class="unsettled" data-unsettled :title="label" :aria-label="label" role="img">
    <Hourglass :size="12" aria-hidden="true" />
  </span>
</template>

<style scoped>
.unsettled {
  display: inline-flex;
  align-items: center;
  margin-left: 4px;
  color: var(--warn);
  vertical-align: middle;
}
</style>
