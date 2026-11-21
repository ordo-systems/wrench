<script setup lang="ts">
// Users lists every configured user with tier, sign-in state and per-window spend (admin only).
// Administrators receive budget and key events for every user, so those refresh the table too.
import { Ban } from "lucide-vue-next";
import { computed, ref } from "vue";
import ConfirmDialog from "../components/ConfirmDialog.vue";
import StatusPill from "../components/StatusPill.vue";
import UnsettledMark from "../components/UnsettledMark.vue";
import TimeCell from "../components/TimeCell.vue";
import { useClient, type BudgetWindow, type UserRow } from "../api";
import * as fmt from "../format";
import { useTopic } from "../live";

const client = useClient();
const rows = ref<UserRow[]>([]);
const warnAt = ref<number[]>([]);
const error = ref("");

const fail = (e: unknown) => (error.value = e instanceof Error ? e.message : String(e));

useTopic(["users", "budget", "keys"], async () => {
  try {
    rows.value = await client.users();
    error.value = "";
  } catch (e) {
    fail(e);
  }
  try {
    warnAt.value = (await client.budget()).warn_at_percent;
  } catch {
    // without thresholds the bars simply stay neutral until 100%
  }
});

const order = ["5_hours", "day", "week", "month"];
// One column per window that any row reports, so the table follows the configured budgets.
const windows = computed(() => {
  const seen = new Set(rows.value.flatMap((u) => u.usage.map((w) => w.window)));
  return order.filter((w) => seen.has(w as BudgetWindow["window"]));
});

const usageOf = (u: UserRow, window: string) => u.usage.find((w) => w.window === window);

function tone(percent: number): string {
  if (percent >= 100) return "bad";
  const lowest = Math.min(...warnAt.value);
  return percent >= lowest ? "warn" : "ok";
}

// The target outlives the dialog closing, because the dialog closes before it emits "confirm".
const target = ref("");
const open = ref(false);
const ask = (email: string) => {
  target.value = email;
  open.value = true;
};

async function confirm() {
  try {
    await client.revokeUser(target.value);
  } catch (e) {
    fail(e);
  }
}
</script>

<template>
  <section class="card">
    <h2>Users</h2>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="!rows.length" class="muted">No users.</p>
    <div v-else class="scroll">
      <table class="users">
        <thead>
          <tr>
            <th>User</th>
            <th>Role</th>
            <th>Tier</th>
            <th>State</th>
            <th class="num">Active keys</th>
            <th>Last key use</th>
            <th v-for="w in windows" :key="w" class="num">{{ fmt.windowName(w) }}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="u in rows" :key="u.email">
            <td>{{ u.email }}</td>
            <td>{{ u.role }}</td>
            <td>{{ u.tier }}</td>
            <td>
              <StatusPill v-if="u.state" :status="u.state.status" />
              <StatusPill v-else status="never signed in" tone="muted" />
            </td>
            <td class="num">{{ u.state?.active_keys ?? fmt.missing }}</td>
            <td><TimeCell :iso="u.state?.last_key_use_at ?? null" /></td>
            <td v-for="w in windows" :key="w" class="spend num">
              <template v-if="usageOf(u, w)">
                <div class="figures">
                  <span
                    >{{ usageOf(u, w)!.percent }}%<UnsettledMark :count="usageOf(u, w)!.unsettled"
                  /></span>
                  <span class="muted">{{ fmt.cents(fmt.nanos(usageOf(u, w)!.spent)) }}</span>
                </div>
                <div class="track">
                  <div
                    class="fill"
                    :class="tone(usageOf(u, w)!.percent)"
                    :style="{ width: `${Math.min(usageOf(u, w)!.percent, 100)}%` }"
                  ></div>
                </div>
              </template>
              <template v-else>{{ fmt.missing }}</template>
            </td>
            <!-- A user who never signed in has nothing to revoke; the API answers 404 for them. -->
            <td>
              <button v-if="u.state" type="button" class="danger" @click="ask(u.email)">
                <Ban :size="14" aria-hidden="true" />Revoke all keys
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <ConfirmDialog
      v-model:open="open"
      title="Revoke user access"
      :message="`Revoke every key and session of ${target}?`"
      confirm-label="Revoke"
      :confirm-icon="Ban"
      @confirm="confirm"
    />
  </section>
</template>

<style scoped>
.spend {
  min-width: 96px;
}
.figures {
  display: flex;
  justify-content: space-between;
  gap: 8px;
}
.track {
  height: 6px;
  margin-top: 3px;
  border: 1px solid var(--line);
  border-radius: 999px;
  overflow: hidden;
}
.fill {
  height: 100%;
  background: var(--ok);
}
.fill.warn {
  background: var(--warn);
}
.fill.bad {
  background: var(--bad);
}
</style>
