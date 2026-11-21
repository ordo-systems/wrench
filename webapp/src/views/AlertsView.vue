<script setup lang="ts">
// Alerts lists incidents, open ones first and then resolved ones, each group newest first (admin only).
// Filters live in the query (?severity=critical|warning, ?status=resolved|all; open is the default) and apply
// in the browser: the list is small.
import { computed, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { CircleCheck, CircleX, TriangleAlert } from "lucide-vue-next";
import { useClient, type Incident } from "../api";
import * as fmt from "../format";
import { useTopic } from "../live";
import StatusPill from "../components/StatusPill.vue";
import InfoDialog from "../components/InfoDialog.vue";
import TimeCell from "../components/TimeCell.vue";

const client = useClient();
const route = useRoute();
const router = useRouter();
const incidents = ref<Incident[]>([]);
const enabled = ref(true);
const error = ref("");

useTopic("alerts", async () => {
  try {
    const answer = await client.alerts("*");
    incidents.value = answer.alerts;
    enabled.value = answer.enabled;
    error.value = "";
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
});

type Severity = "" | Incident["severity"];
type Status = "open" | "resolved" | "all";

// A query-backed select value; an absent or unknown value falls back to the default.
function queryParam<T extends string>(name: string, allowed: readonly T[], fallback: T) {
  return computed<T>({
    get: () => {
      const v = route.query[name];
      return allowed.includes(v as T) ? (v as T) : fallback;
    },
    set: (v) => {
      const { [name]: _, ...rest } = route.query;
      void router.replace({ query: v === fallback ? rest : { ...rest, [name]: v } });
    },
  });
}
const severity = queryParam<Severity>("severity", ["", "critical", "warning"], "");
const status = queryParam<Status>("status", ["open", "resolved", "all"], "open");

// Array.sort is stable, so the server's order holds inside each group.
const ordered = computed(() =>
  incidents.value
    .filter((a) => !severity.value || a.severity === severity.value)
    .filter((a) => status.value === "all" || (status.value === "resolved") === (a.resolved_at !== null))
    .sort((a, b) => Number(a.resolved_at !== null) - Number(b.resolved_at !== null)),
);

// The incident shown in the details dialog. It stays set while the dialog closes, so the content does not
// vanish mid-close.
const selected = ref<Incident | null>(null);
const detailsOpen = ref(false);
function show(a: Incident) {
  selected.value = a;
  detailsOpen.value = true;
}
// Enter and Space open the details of a focused row, like a button.
function onKey(e: KeyboardEvent, a: Incident) {
  if (e.key !== "Enter" && e.key !== " ") return;
  e.preventDefault();
  show(a);
}
</script>

<template>
  <section class="card">
    <h2>Alerts</h2>
    <div class="toolbar">
      <select v-model="severity" aria-label="Severity">
        <option value="">All severities</option>
        <option value="critical">Critical</option>
        <option value="warning">Warning</option>
      </select>
      <select v-model="status" aria-label="Status">
        <option value="open">Open</option>
        <option value="resolved">Resolved</option>
        <option value="all">All</option>
      </select>
    </div>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="!enabled" class="muted">Alert checks are not configured; only status alerts are shown.</p>
    <p v-if="!ordered.length" class="muted">No incidents.</p>
    <div v-else class="scroll">
      <table class="alerts">
        <colgroup>
          <col class="c-sev" />
          <col class="c-status" />
          <col class="c-user" />
          <col />
          <col class="c-date" />
          <col class="c-date" />
          <col class="c-date" />
        </colgroup>
        <thead>
          <tr>
            <th>Severity</th>
            <th>Status</th>
            <th>User</th>
            <th>Message</th>
            <th>Opened</th>
            <th>Last seen</th>
            <th>Resolved</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="a in ordered"
            :key="a.id"
            :class="{ resolved: a.resolved_at !== null }"
            tabindex="0"
            aria-haspopup="dialog"
            @click="show(a)"
            @keydown="onKey($event, a)"
          >
            <td class="severity"><StatusPill :status="a.severity" /></td>
            <td class="state">
              <span v-if="a.resolved_at !== null" class="pill status muted">
                <CircleCheck :size="12" aria-hidden="true" />Resolved
              </span>
              <span v-else class="pill status" :class="a.severity === 'critical' ? 'bad' : 'warn'">
                <CircleX v-if="a.severity === 'critical'" :size="12" aria-hidden="true" />
                <TriangleAlert v-else :size="12" aria-hidden="true" />Open
              </span>
            </td>
            <td class="user">{{ a.user_email || fmt.missing }}</td>
            <td class="message trim" :title="a.message">
              {{ a.message }}
            </td>
            <td><TimeCell :iso="a.opened_at" compact /></td>
            <td><TimeCell :iso="a.last_seen_at" compact /></td>
            <td><TimeCell :iso="a.resolved_at" compact /></td>
          </tr>
        </tbody>
      </table>
    </div>
    <InfoDialog v-model:open="detailsOpen" title="Alert details">
      <dl v-if="selected" class="details">
        <dt>Severity</dt>
        <dd><StatusPill :status="selected.severity" /></dd>
        <dt>Status</dt>
        <dd>
          <span v-if="selected.resolved_at !== null" class="pill status muted">
            <CircleCheck :size="12" aria-hidden="true" />Resolved
          </span>
          <span v-else class="pill status" :class="selected.severity === 'critical' ? 'bad' : 'warn'">
            <CircleX v-if="selected.severity === 'critical'" :size="12" aria-hidden="true" />
            <TriangleAlert v-else :size="12" aria-hidden="true" />Open
          </span>
        </dd>
        <dt>User</dt>
        <dd>{{ selected.user_email || fmt.missing }}</dd>
        <dt>Message</dt>
        <dd>{{ selected.message }}</dd>
        <dt>Key</dt>
        <dd class="mono">{{ selected.key }}</dd>
        <dt>Opened</dt>
        <dd><TimeCell :iso="selected.opened_at" /></dd>
        <dt>Last seen</dt>
        <dd><TimeCell :iso="selected.last_seen_at" /></dd>
        <dt>Resolved</dt>
        <dd><TimeCell :iso="selected.resolved_at" /></dd>
      </dl>
    </InfoDialog>
  </section>
</template>

<style scoped>
.toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 12px;
}
/* Fixed layout: Message takes the remaining width; below 720 px the table keeps a minimum and scrolls.
   Date columns hold the compact time ("Nov 19, 22:51") with the relative time on the line below. */
.alerts {
  table-layout: fixed;
  min-width: 720px;
}
.c-sev {
  width: 100px;
}
.c-status {
  width: 100px;
}
.c-user {
  width: 150px;
}
.c-date {
  width: 150px;
}
.alerts td {
  overflow: hidden;
  text-overflow: ellipsis;
}
.alerts tbody tr {
  cursor: pointer;
}
.alerts tbody tr:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: -2px;
}
.alerts tbody tr.resolved {
  opacity: 0.6;
}
.alerts td.trim {
  white-space: nowrap;
}
.details {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 8px 16px;
  margin: 0;
}
.details dt {
  color: var(--muted);
}
.details dd {
  margin: 0;
  overflow-wrap: anywhere;
}
.alerts :deep(.rel) {
  display: block;
}
.status {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
</style>
