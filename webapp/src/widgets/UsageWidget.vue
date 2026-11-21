<script setup lang="ts">
// UsageWidget shows the six usage cards with the period, their current value and an activity grid of the
// period's buckets: the Usage tab's ("me": the caller, or for an administrator the user picked in the filter, "*"
// for everyone) or everyone's on Overview ("system"). Each card is one link to its detail view with the same period, model and user.
import { computed } from "vue";
import { useRoute } from "vue-router";
import ActivityGrid from "../components/ActivityGrid.vue";
import UsageFilters from "../components/UsageFilters.vue";
import type { Scope } from "../api";
import { useStats } from "../live";
import { cardValue, cellValues, metrics, periodOf, type MetricId } from "../metrics";

const props = defineProps<{ scope: "me" | "system"; user?: Scope }>();

const route = useRoute();
const period = computed(() => periodOf(route.query.period));
const model = computed(() => String(route.query.model ?? ""));
const base = computed(() => (props.scope === "me" ? "/usage" : "/overview"));
// Overview always shows everyone; the Usage tab shows whoever the filter picked.
const user = computed(() => (props.scope === "system" ? "*" : (props.user ?? "")));
const title = computed(() => {
  if (user.value === "*") return "System usage";
  return user.value ? `Usage of ${user.value}` : "Your usage";
});

const { stats, error } = useStats(() => ({ period: period.value, model: model.value, user: user.value }));

function buckets(id: MetricId) {
  if (!stats.value) return [];
  const values = cellValues(id, stats.value);
  return stats.value.buckets.map((b, i) => ({ at: b.start, value: values[i] ?? null }));
}

function linkTo(id: string) {
  const query: Record<string, string> = { period: period.value };
  if (model.value) query.model = model.value;
  if (props.scope === "me" && user.value) query.user = user.value;
  return { path: `${base.value}/${id}`, query };
}
</script>

<template>
  <section class="card usage">
    <div class="head">
      <h2>{{ title }}</h2>
      <UsageFilters :system="user !== ''" />
    </div>
    <p v-if="error" class="error">{{ error }}</p>
    <div class="cards">
      <RouterLink v-for="m in metrics" :key="m.id" :to="linkTo(m.id)" class="metric clickable-card" data-card>
        <span class="card-head">
          <span class="title">{{ m.title }}</span>
          <small class="muted" data-period>{{ period }}</small>
        </span>
        <strong class="value mono">{{ stats ? cardValue(m.id, stats) : "…" }}</strong>
        <small v-if="m.id === 'spend' && stats && stats.total.pending > 0" class="warn">
          + {{ stats.total.pending }} with unknown cost
        </small>
        <ActivityGrid v-if="stats" :buckets="buckets(m.id)" :period="period" :format="m.format" />
      </RouterLink>
    </div>
  </section>
</template>

<style scoped>
.head {
  display: flex;
  flex-wrap: wrap;
  justify-content: space-between;
  align-items: flex-end;
  gap: 12px;
  margin-bottom: 12px;
}
.head h2 {
  margin: 0;
}
.cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 12px;
}
.metric {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 14px;
  border: 1px solid var(--line);
  border-radius: 10px;
}
.card-head {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  gap: 8px;
  font-size: 13px;
}
.card-head small {
  font-size: 12px;
}
.value {
  font-size: 24px;
  text-align: right;
}
.metric small {
  font-size: 13px;
}
</style>
