<script setup lang="ts">
// AlertBanner shows the open critical incidents the server allows the caller to see (Status.alerts).
// The texts arrive worded by the server; only administrators get a link to the Alerts tab.
import type { Status } from "../api";

defineProps<{ alerts: Status["alerts"]; admin: boolean }>();
</script>

<template>
  <div v-if="alerts.length" class="banner" data-banner role="alert">
    <ul>
      <li v-for="a in alerts" :key="a.key">{{ a.message }}</li>
    </ul>
    <RouterLink v-if="admin" to="/alerts">Open alerts</RouterLink>
  </div>
</template>

<style scoped>
.banner {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 24px;
  border-bottom: 1px solid var(--bad);
  background: color-mix(in srgb, var(--bad) 14%, var(--page));
  color: var(--bad);
}
ul {
  margin: 0;
  padding: 0;
  list-style: none;
  font-weight: 600;
}
a {
  color: inherit;
  white-space: nowrap;
}
</style>
