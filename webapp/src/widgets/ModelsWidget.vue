<script setup lang="ts">
// ModelsWidget lists the models a user may select (their tier, with context length, the default and the
// reasoning mark, the catalog price per 1M tokens and the caller's requests and spend in the current day budget
// window, the spend drawn as a bar against the day's largest). Administrators see every configured model from
// /api/system with its context and prices; route health sits in the row tooltip, errors surface on Alerts.
// Config reloads publish "status", so that topic refreshes the models; "usage" refreshes the day's figures.
import { computed, ref } from "vue";
import { useClient, type ModelInfo, type ModelList, type UsageTotals } from "../api";
import * as fmt from "../format";
import { useTopic } from "../live";

const props = defineProps<{ admin: boolean }>();

const client = useClient();
const list = ref<ModelList | null>(null);
const all = ref<ModelInfo[] | null>(null);
const error = ref("");
const today = ref<Map<string, UsageTotals>>(new Map());

useTopic("status", async () => {
  try {
    if (props.admin) {
      const system = await client.system();
      all.value = system.models ?? [];
    } else list.value = await client.models();
    error.value = "";
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
});

if (!props.admin)
  useTopic("usage", async () => {
    try {
      const usage = await client.usage("day", "model");
      today.value = new Map(usage.groups.map((g) => [g.key ?? "", g]));
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e);
    }
  });

const spendOf = (name: string) => fmt.nanos(today.value.get(name)?.cost ?? "0");
const maxSpend = computed(() =>
  (list.value?.models ?? []).reduce((m, x) => (spendOf(x.name) > m ? spendOf(x.name) : m), 0n),
);
// spendShare is a model's spend as a percent of the day's largest, for the bar width.
const spendShare = (name: string) =>
  maxSpend.value > 0n ? Number((spendOf(name) * 100n) / maxSpend.value) : 0;

// details is the row tooltip: upstream id, attempts and the last success and error in the health window.
function details(m: ModelInfo): string {
  const h = m.health;
  const error = h?.last_error_at
    ? `${fmt.time(h.last_error_at)}${h.last_error_reason ? ` (${h.last_error_reason})` : ""}`
    : fmt.missing;
  return [
    m.upstream_id ? `Upstream: ${m.upstream_id}` : "",
    `Attempts: ${h?.attempts ?? 0}`,
    `Last success: ${fmt.optionalTime(h?.last_success_at ?? null)}`,
    `Last error: ${error}`,
  ]
    .filter(Boolean)
    .join("\n");
}
</script>

<template>
  <section class="card">
    <h2>Models</h2>
    <p v-if="!admin" class="muted note-under-title">Models you may use with Wrench.</p>
    <p v-if="error" class="error">{{ error }}</p>

    <div v-if="list" class="scroll">
      <table>
        <thead>
          <tr>
            <th>Model</th>
            <th class="num">Context</th>
            <th class="num">Price in</th>
            <th class="num">Price out</th>
            <th class="num">Requests today</th>
            <th class="num">Spend today</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="m in list.models" :key="m.name" :data-default="m.name === list.default || undefined">
            <td>
              {{ m.name }}
              <span v-if="m.name === list.default" class="pill ok">default</span>
              <span v-if="m.reasoning" class="pill">reasoning</span>
            </td>
            <td class="num">{{ fmt.contextSize(m.input_token_limit) }}</td>
            <td class="num">
              {{ fmt.price(m.price_input_per_mtok)
              }}<small v-if="m.price_input_per_mtok !== null" class="muted">/1M tokens</small>
            </td>
            <td class="num">
              {{ fmt.price(m.price_output_per_mtok)
              }}<small v-if="m.price_output_per_mtok !== null" class="muted">/1M tokens</small>
            </td>
            <td class="num">{{ today.get(m.name)?.attempts ?? 0 }}</td>
            <td class="num">
              <span class="spend">
                <span class="bar"><span data-spend-bar :style="{ width: `${spendShare(m.name)}%` }" /></span>
                {{ fmt.dollars(today.get(m.name)?.cost ?? "0") }}
              </span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="all" class="scroll">
      <table>
        <thead>
          <tr>
            <th>Model</th>
            <th class="num">Context</th>
            <th class="num">Price in</th>
            <th class="num">Price out</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="m in all" :key="m.name" :title="details(m)">
            <td>
              {{ m.name }}
              <span v-if="m.reasoning_effort" class="pill">reasoning</span>
            </td>
            <td class="num">{{ fmt.contextSize(m.input_token_limit) }}</td>
            <td class="num">
              {{ fmt.price(m.price_input_per_mtok)
              }}<small v-if="m.price_input_per_mtok !== null" class="muted">/1M tokens</small>
            </td>
            <td class="num">
              {{ fmt.price(m.price_output_per_mtok)
              }}<small v-if="m.price_output_per_mtok !== null" class="muted">/1M tokens</small>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.note-under-title {
  margin: -8px 0 12px;
  font-size: 13px;
}
/* The unit after a price: smaller and faint, so the amounts still read as one column. */
td small {
  margin-left: 1px;
  font-size: 11px;
}
/* The flex box sits inside the cell: a flex table cell would drop out of the row's borders. */
.spend {
  display: inline-flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
}
.bar {
  flex: 0 0 60px;
  height: 6px;
  border-radius: 3px;
  background: var(--line);
  overflow: hidden;
}
.bar span {
  display: block;
  height: 100%;
  background: var(--accent);
}
</style>
