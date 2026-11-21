<script setup lang="ts">
// MetricDetailView is the full chart of one usage card (/overview/:metric for the system, /usage/:metric for
// the caller), with the period, model and, for an administrator on Usage, the user filter from the query.
// Whenever the chart covers all users an administrator can split it by user; Confirmed spend also carries the
// cost breakdowns (in a row of three cards, as on Overview) and session costs.
import { computed, ref } from "vue";
import { useRoute } from "vue-router";
import Chart from "../components/Chart.vue";
import UsageFilters from "../components/UsageFilters.vue";
import UserSelect from "../components/UserSelect.vue";
import * as fmt from "../format";
import { useStats } from "../live";
import { cardSeries, facts, isMetric, metric, periodOf, userSeries } from "../metrics";
import { useUserFilter } from "../userFilter";

const route = useRoute();
const filter = useUserFilter();

const id = computed(() => {
  const m = String(route.params.metric ?? "");
  return isMetric(m) ? m : null;
});
const parent = computed(() => (route.path.startsWith("/overview") ? "/overview" : "/usage"));
const system = computed(() => parent.value === "/overview");
// The system view asks for everyone ("*"); on Usage the filter decides, and without a user the server answers
// for the caller alone.
const user = computed(() => (system.value ? "*" : filter.user.value));
const canSplit = computed(() => filter.admin.value && user.value === "*");
const byUser = ref(false);

const { stats, error } = useStats(() => ({
  period: periodOf(route.query.period),
  model: String(route.query.model ?? ""),
  user: user.value,
  groupBy: canSplit.value && byUser.value ? "user" : undefined,
}));

const series = computed(() => {
  if (!stats.value || !id.value) return [];
  return byUser.value && canSplit.value
    ? userSeries(id.value, stats.value)
    : cardSeries(id.value, stats.value);
});

const back = computed(() => ({ path: parent.value, query: route.query }));
const breakdowns = computed(() =>
  stats.value
    ? ([
        ["By user", stats.value.by_user],
        ["By model", stats.value.by_model],
        ["By inference provider", stats.value.by_provider],
      ] as const)
    : [],
);
</script>

<template>
  <section v-if="!id" class="card">
    <p>Unknown metric. <RouterLink :to="back">Back</RouterLink></p>
  </section>
  <div v-else class="grid">
    <section class="card wide">
      <div class="head">
        <div>
          <RouterLink :to="back" class="muted">← {{ system ? "Overview" : "Usage" }}</RouterLink>
          <h2>{{ metric(id).title }}</h2>
        </div>
        <UsageFilters :system="user !== ''">
          <label v-if="filter.admin.value && !system">
            User
            <UserSelect v-model="filter.user.value" :emails="filter.emails.value" :me="filter.me.value" />
          </label>
          <label v-if="canSplit" class="toggle" data-by-user>
            <input v-model="byUser" type="checkbox" />
            By user
          </label>
        </UsageFilters>
      </div>
      <p v-if="error" class="error">{{ error }}</p>
      <Chart
        v-if="stats"
        :label="metric(id).title"
        :starts="stats.buckets.map((b) => b.start)"
        :series="series"
        :kind="metric(id).kind"
        :format="metric(id).format"
      />
      <p v-if="metric(id).note" class="muted">{{ metric(id).note }}</p>
      <p v-if="byUser && canSplit && !metric(id).additive" class="muted">
        Percentiles and shares do not add up, so other users are not drawn.
      </p>
      <dl v-if="stats" class="facts">
        <div v-for="f in facts(id, stats)" :key="f.label" class="fact-card">
          <dt class="fact-title">{{ f.label }}</dt>
          <dd class="fact-value mono accent" :title="f.title">{{ f.value }}</dd>
          <dd v-if="f.note" class="fact-note muted">{{ f.note }}</dd>
        </div>
      </dl>
    </section>

    <div v-if="id === 'spend' && breakdowns.length" class="row-3 wide">
      <section v-for="[title, rows] in breakdowns" :key="title" class="card scroll">
        <h2>{{ title }}</h2>
        <table>
          <thead>
            <tr>
              <th></th>
              <th>Attempts</th>
              <th>Confirmed cost</th>
              <th>Unknown</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in rows" :key="r.key">
              <td>{{ r.key || "not reported" }}</td>
              <td class="mono">{{ r.attempts }}</td>
              <td class="mono" :title="fmt.cost(r.cost)">{{ fmt.dollars(r.cost) }}</td>
              <td class="mono" :class="{ warn: r.pending > 0 }">{{ r.pending }}</td>
            </tr>
          </tbody>
        </table>
      </section>
    </div>
  </div>
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
  margin: 4px 0 0;
}
.head a {
  text-decoration: none;
  font-size: 13px;
}
.toggle {
  flex-direction: row;
  align-items: center;
  gap: 6px;
  padding-bottom: 4px;
}
/* Equal cards in a full-width grid: the title, the value large and right-aligned, a faint note below. */
.facts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  grid-auto-rows: 1fr;
  gap: 12px;
  margin: 16px 0 0;
}
.fact-card {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 12px 14px;
  border: 1px solid var(--line);
  border-radius: 10px;
}
.facts dd {
  margin: 0;
}
.fact-title {
  font-size: 13px;
}
.fact-value {
  font-size: 22px;
  text-align: right;
  color: var(--accent);
}
.fact-note {
  font-size: 12px;
  text-align: right;
}
</style>
