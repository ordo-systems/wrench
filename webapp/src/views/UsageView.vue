<script setup lang="ts">
// Usage is everyone's own view: their tier's models (two thirds of the row), their budget (one third) and their
// usage; whether requests run is in the top bar. Administrators can pick another user (their budget and usage) or
// all users (the shared budget and everyone's usage) in the user filter; the system-wide status and models are on
// Overview. The Models card always shows the caller's own tier and day.
import UserSelect from "../components/UserSelect.vue";
import { useUserFilter } from "../userFilter";
import BudgetWidget from "../widgets/BudgetWidget.vue";
import ModelsWidget from "../widgets/ModelsWidget.vue";
import UsageWidget from "../widgets/UsageWidget.vue";

const { admin, user, emails, me } = useUserFilter();
</script>

<template>
  <div class="grid">
    <div v-if="admin" class="wide toolbar">
      <label>User <UserSelect v-model="user" :emails="emails" :me="me" /></label>
    </div>
    <div class="row-2-1 wide">
      <ModelsWidget :admin="false" />
      <BudgetWidget v-if="user === '*'" scope="global" />
      <BudgetWidget v-else scope="user" :user="user" />
    </div>
    <UsageWidget class="wide" scope="me" :user="user" />
  </div>
</template>

<style scoped>
.toolbar label {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--muted);
}
</style>
