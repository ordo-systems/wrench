<script setup lang="ts">
// UsageFilters are the Period and Model selects of the usage cards and the detail view. Both live in the route
// query, so a view is a shareable link; the model list is the caller's tier, or every model for the system.
import { onMounted, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useClient } from "../api";
import { periodOf, periods } from "../metrics";

const props = defineProps<{ system: boolean }>();

const route = useRoute();
const router = useRouter();
const client = useClient();
const models = ref<string[]>([]);

onMounted(async () => {
  try {
    models.value = props.system
      ? ((await client.system()).models ?? []).map((m) => m.name)
      : (await client.models()).models.map((m) => m.name);
  } catch {
    // the select still offers all models
  }
});

function set(key: "period" | "model", value: string) {
  const query = { ...route.query, [key]: value };
  if (!value) delete query[key];
  void router.replace({ query });
}
</script>

<template>
  <form class="filters" @submit.prevent>
    <label>
      Period
      <select
        :value="periodOf(route.query.period)"
        @change="set('period', ($event.target as HTMLSelectElement).value)"
      >
        <option v-for="p in periods" :key="p.value" :value="p.value">{{ p.label }}</option>
      </select>
    </label>
    <label>
      Model
      <select
        :value="String(route.query.model ?? '')"
        @change="set('model', ($event.target as HTMLSelectElement).value)"
      >
        <option value="">All models</option>
        <option v-for="m in models" :key="m" :value="m">{{ m }}</option>
      </select>
    </label>
    <slot />
  </form>
</template>

<style scoped>
.filters {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 12px;
}
label {
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: 13px;
  color: var(--muted);
}
</style>
