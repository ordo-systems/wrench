<script setup lang="ts">
// SessionsTable lists sessions in the order given (the server sorts by last seen, newest first).
import { computed } from "vue";
import { ArrowDown } from "lucide-vue-next";
import type { Session } from "../api";
import * as fmt from "../format";
import CopyButton from "./CopyButton.vue";
import StatusPill from "./StatusPill.vue";
import TimeCell from "./TimeCell.vue";

const props = defineProps<{ rows: Session[]; showUser: boolean }>();

// The Unconfirmed column appears only when some session still waits for a final price.
const showPending = computed(() => props.rows.some((s) => s.usage.pending > 0));

const short = (id: string) => id.slice(0, 12);
</script>

<template>
  <p v-if="rows.length === 0">No sessions.</p>
  <table v-else class="sessions">
    <thead>
      <tr>
        <th>Status</th>
        <th>Session</th>
        <th v-if="showUser">User</th>
        <th>Agent</th>
        <th>Device</th>
        <th>Started</th>
        <th aria-sort="descending">
          <span class="sorted">Last seen<ArrowDown class="sort-icon" :size="12" aria-hidden="true" /></span>
        </th>
        <th class="num">Duration</th>
        <th class="num">Requests</th>
        <th class="num">Cost</th>
        <th
          v-if="showPending"
          class="num"
          title="Requests whose final price OpenRouter has not confirmed yet"
        >
          Unconfirmed
        </th>
      </tr>
    </thead>
    <tbody>
      <tr v-for="s in rows" :key="s.id">
        <td><StatusPill :status="s.status" /></td>
        <td>
          <span class="id-cell">
            <span class="mono session-id" :title="s.id">{{ short(s.id) }}</span>
            <CopyButton class="copy" :text="s.id" label="Copy session id" icon-only />
          </span>
        </td>
        <td v-if="showUser">{{ s.user_email }}</td>
        <td class="mono">{{ s.agent || fmt.missing }}</td>
        <td>{{ s.device_name || s.device_id || fmt.missing }}</td>
        <td><TimeCell :iso="s.started_at" /></td>
        <td><TimeCell :iso="s.last_seen_at" /></td>
        <td class="num">{{ fmt.age(s.started_at, s.last_seen_at) }}</td>
        <td class="num">{{ s.usage.attempts }}</td>
        <td class="num cost" :title="fmt.cost(s.usage.cost)">{{ fmt.dollars(s.usage.cost) }}</td>
        <td v-if="showPending" class="num" :class="{ warn: s.usage.pending > 0 }">{{ s.usage.pending }}</td>
      </tr>
    </tbody>
  </table>
</template>

<style scoped>
.sorted {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
/* The trimmed id and its copy button sit side by side and never overlap. */
.id-cell {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
}
/* The copy button is a compact icon button inside CopyButton, so the rule reaches into it. */
.id-cell :deep(.copy) {
  flex: none;
  padding: 2px;
  border-radius: 6px;
}
.id-cell :deep(.copy[data-state="idle"]) {
  color: var(--muted);
}
</style>
