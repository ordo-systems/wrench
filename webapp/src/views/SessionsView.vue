<script setup lang="ts">
// Sessions lists the caller's sessions; administrators can look at one user or everyone.
// The filters live in the query: ?status=active|offline and ?user=<email>|*.
import { computed, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import SessionsTable from "../components/SessionsTable.vue";
import UserSelect from "../components/UserSelect.vue";
import { useClient, type Session, type SessionStatus } from "../api";
import { useTopic } from "../live";
import { useUserFilter } from "../userFilter";

const client = useClient();
const route = useRoute();
const router = useRouter();
const { admin, user, emails, me } = useUserFilter();
const rows = ref<Session[]>([]);
const error = ref("");

const status = computed<"" | SessionStatus>({
  get: () => {
    const v = route.query.status;
    return v === "active" || v === "offline" ? v : "";
  },
  set: (v) => {
    const { status: _, ...rest } = route.query;
    void router.replace({ query: v ? { ...rest, status: v } : rest });
  },
});

// A stale answer for a previous filter never replaces the current one.
let gen = 0;
async function load() {
  const mine = ++gen;
  try {
    const list = await client.sessions(50, user.value, status.value || undefined);
    if (mine !== gen) return;
    rows.value = list;
    error.value = "";
  } catch (e) {
    if (mine === gen) error.value = e instanceof Error ? e.message : String(e);
  }
}

useTopic("sessions", load);
watch([user, status], load);
</script>

<template>
  <section class="card">
    <h2>Sessions</h2>
    <div class="toolbar">
      <UserSelect v-if="admin" v-model="user" :emails="emails" :me="me" />
      <select v-model="status" aria-label="Status">
        <option value="">All</option>
        <option value="active">Active</option>
        <option value="offline">Offline</option>
      </select>
    </div>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <div class="scroll"><SessionsTable :rows="rows" :show-user="admin" /></div>
  </section>
</template>

<style scoped>
.toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 12px;
}
</style>
