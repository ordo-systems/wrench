<script setup lang="ts">
// Keys lists device keys: the caller's own, or, for administrators, one user's (?user=<email>) or everyone's
// (?user=*). Every revocation goes through a confirmation dialog.
import { Ban, Plus } from "lucide-vue-next";
import { computed, ref, watch } from "vue";
import ConfirmDialog from "../components/ConfirmDialog.vue";
import DeviceKeyHelp from "../components/DeviceKeyHelp.vue";
import KeysTable from "../components/KeysTable.vue";
import UserSelect from "../components/UserSelect.vue";
import { useClient, type ClientKey } from "../api";
import * as fmt from "../format";
import { useTopic } from "../live";
import { useUserFilter } from "../userFilter";

const client = useClient();
const { admin, user, emails, me } = useUserFilter();
const rows = ref<ClientKey[]>([]);
const error = ref("");
const help = ref(false);

const fail = (e: unknown) => (error.value = e instanceof Error ? e.message : String(e));

// A stale answer for a previous filter never replaces the current one.
let gen = 0;
async function load() {
  const mine = ++gen;
  try {
    const keys = await client.keys(user.value);
    if (mine !== gen) return;
    rows.value = keys;
    error.value = "";
  } catch (e) {
    if (mine === gen) fail(e);
  }
}

const activeCount = computed(() => rows.value.filter((k) => fmt.keyState(k) === "active").length);
// "Revoke all" targets one user's keys, so it is hidden for "All users" and when nothing is active.
const canRevokeAll = computed(() => user.value !== "*" && activeCount.value > 0);

// What the open dialog will revoke: one key, or every active key of the shown user.
// The target outlives the dialog closing, because the dialog closes before it emits "confirm".
const target = ref<ClientKey | "all" | null>(null);
const open = ref(false);
const ask = (t: ClientKey | "all") => {
  target.value = t;
  open.value = true;
};
const title = computed(() => (target.value === "all" ? "Revoke all keys" : "Revoke key"));
const message = computed(() => {
  const t = target.value;
  if (t === "all") return `Revoke ${activeCount.value} active ${activeCount.value === 1 ? "key" : "keys"}?`;
  return t ? `Revoke the key of ${t.device_name || t.device_id}?` : "";
});

async function confirm() {
  const t = target.value;
  try {
    if (t === "all") await client.revokeKeys(user.value);
    else if (t) await client.revokeKey(t.id);
    await load();
  } catch (e) {
    fail(e);
  }
}

useTopic("keys", load);
watch(user, load);
</script>

<template>
  <section class="card">
    <h2>Device keys</h2>
    <div class="toolbar">
      <UserSelect v-if="admin" v-model="user" :emails="emails" :me="me" />
      <button v-if="!admin || user === ''" type="button" class="primary" @click="help = true">
        <Plus :size="14" aria-hidden="true" />New device key
      </button>
      <button v-if="canRevokeAll" type="button" class="danger" @click="ask('all')">
        <Ban :size="14" aria-hidden="true" />Revoke all
      </button>
    </div>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <div class="scroll">
      <KeysTable :rows="rows" :show-user="admin && user === '*'" @revoke-key="(k) => ask(k)" />
    </div>
    <DeviceKeyHelp v-model:open="help" />
    <ConfirmDialog
      v-model:open="open"
      :title="title"
      :message="message"
      confirm-label="Revoke"
      :confirm-icon="Ban"
      @confirm="confirm"
    />
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
