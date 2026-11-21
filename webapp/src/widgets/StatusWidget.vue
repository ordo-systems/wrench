<script setup lang="ts">
// StatusWidget says whether the caller's inference can run and, if not, why. Administrators also see the
// system checks, the configuration revision, backup and accounting health. Models with their route health are
// on the Models card, tier budgets on the Tiers card.
import { ref } from "vue";
import { useClient, type Status, type SystemInfo } from "../api";
import TimeCell from "../components/TimeCell.vue";
import * as fmt from "../format";
import { useTopic } from "../live";

const props = defineProps<{ admin: boolean }>();

const client = useClient();
const status = ref<Status | null>(null);
const system = ref<SystemInfo | null>(null);
const error = ref("");
const systemError = ref("");

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

// The two answers load independently: during an outage /api/system may fail while /api/status still explains
// why requests are paused.
useTopic("status", async () => {
  const [s, sys] = await Promise.allSettled([client.status(), props.admin ? client.system() : null]);
  if (s.status === "fulfilled") {
    status.value = s.value;
    error.value = "";
  } else error.value = message(s.reason);
  if (sys.status === "fulfilled") {
    system.value = sys.value;
    systemError.value = "";
  } else systemError.value = `System checks: ${message(sys.reason)}`;
});

const ok = (v: string | undefined) => v === "ok";
</script>

<template>
  <section class="card">
    <h2>Status</h2>
    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="systemError" class="error">{{ systemError }}</p>
    <template v-if="status">
      <!-- Running is the quiet default (the top bar says so); only a pause gets a pill. -->
      <p v-if="status.inference.state !== 'ok'"><span class="pill bad">Requests paused</span></p>
      <ul v-if="status.inference.reasons.length" class="reasons">
        <li v-for="r in status.inference.reasons" :key="r.code">{{ fmt.pauseReason(r) }}</li>
      </ul>
    </template>

    <template v-if="system">
      <table data-checks>
        <tbody>
          <tr v-for="(v, k) in system.checks" :key="k">
            <th>{{ k }}</th>
            <td>
              <span class="pill" :class="ok(v) ? 'ok' : 'bad'">{{ v }}</span>
            </td>
          </tr>
          <tr>
            <th>backup</th>
            <td>
              <span class="pill" :class="ok(system.backup?.status) ? 'ok' : 'warn'">{{
                system.backup?.status ?? fmt.missing
              }}</span>
            </td>
          </tr>
          <tr v-if="system.backup?.last_success_at">
            <th>last backup</th>
            <td><TimeCell :iso="system.backup.last_success_at" /></td>
          </tr>
          <tr v-if="system.configuration">
            <th>config revision</th>
            <td class="mono">{{ system.configuration.revision.slice(0, 19) }}</td>
          </tr>
          <tr v-if="system.version">
            <th>version</th>
            <td class="mono">{{ system.version }}</td>
          </tr>
        </tbody>
      </table>
      <p v-if="system.backup?.problem" class="warn">Backup: {{ system.backup.problem }}</p>
      <p v-if="system.configuration?.error" class="warn">
        Configuration change rejected: {{ system.configuration.error }}
      </p>
    </template>
  </section>
</template>

<style scoped>
.reasons {
  margin: 0 0 8px;
  padding-left: 18px;
}
</style>
