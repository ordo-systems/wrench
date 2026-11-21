<script setup lang="ts">
import { Ban } from "lucide-vue-next";
import type { ClientKey } from "../api";
import * as fmt from "../format";
import StatusPill from "./StatusPill.vue";
import TimeCell from "./TimeCell.vue";

// KeysTable lists device keys; an active key has a Revoke button (the parent confirms before revoking).
defineProps<{ rows: ClientKey[]; showUser: boolean }>();
const origin = location.origin;
defineEmits<{ "revoke-key": [key: ClientKey] }>();
</script>

<template>
  <p v-if="rows.length === 0">
    No device keys. Sign in from opencode with <code>{{ fmt.deviceLoginCommand(origin) }}</code
    >.
  </p>
  <table v-else class="keys">
    <thead>
      <tr>
        <th v-if="showUser">User</th>
        <th>Device</th>
        <th>Created</th>
        <th>Expires</th>
        <th>Last used</th>
        <th>State</th>
        <th class="row-action"></th>
      </tr>
    </thead>
    <tbody>
      <tr v-for="k in rows" :key="k.id">
        <td v-if="showUser">{{ k.user_email }}</td>
        <td>{{ k.device_name || k.device_id }}</td>
        <td><TimeCell :iso="k.created_at" /></td>
        <td><TimeCell :iso="k.expires_at" /></td>
        <td><TimeCell :iso="k.last_used_at" /></td>
        <td><StatusPill :status="fmt.keyState(k)" /></td>
        <td class="row-action">
          <button
            v-if="fmt.keyState(k) === 'active'"
            type="button"
            class="danger small"
            @click="$emit('revoke-key', k)"
          >
            <Ban :size="12" aria-hidden="true" />Revoke
          </button>
        </td>
      </tr>
    </tbody>
  </table>
</template>

<style scoped>
/* The action column has a fixed width and a compact button, so every row keeps the same height. */
.row-action {
  width: 88px;
  text-align: right;
}
</style>
