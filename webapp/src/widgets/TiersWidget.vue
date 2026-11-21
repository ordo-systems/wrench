<script setup lang="ts">
// TiersWidget lists the configured tiers for administrators: each tier's models, its default model, whether it
// is the default tier and its budget per window. Config reloads publish "status", so that topic refreshes it.
import { ref } from "vue";
import { useClient, type TierInfo } from "../api";
import * as fmt from "../format";
import { useTopic } from "../live";

const client = useClient();
const tiers = ref<TierInfo[] | null>(null);
const error = ref("");

useTopic("status", async () => {
  try {
    tiers.value = (await client.system()).tiers ?? [];
    error.value = "";
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
});

const windows = ["5_hours", "day", "week", "month"] as const;
const defaultModel = (t: TierInfo) => t.models.find((m) => m.default)?.name ?? fmt.missing;
</script>

<template>
  <section class="card">
    <h2>Tiers</h2>
    <p v-if="error" class="error">{{ error }}</p>
    <p v-else-if="tiers && !tiers.length" class="muted">No tiers are configured.</p>
    <div v-if="tiers?.length" class="scroll">
      <table>
        <thead>
          <tr>
            <th>Tier</th>
            <th>Models</th>
            <th>Default model</th>
            <th v-for="w in windows" :key="w" class="num">{{ fmt.windowName(w) }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="t in tiers" :key="t.name">
            <td>
              {{ t.name }}
              <span v-if="t.default" class="pill ok" data-default-tier>default</span>
            </td>
            <td class="models">{{ t.models.map((m) => m.name).join(", ") || fmt.missing }}</td>
            <td data-default-model>{{ defaultModel(t) }}</td>
            <td v-for="w in windows" :key="w" class="num">{{ fmt.dollars(t.budget[w]) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
/* The model list wraps, so the budget columns fit a half-width card. */
.models {
  white-space: normal;
}
</style>
