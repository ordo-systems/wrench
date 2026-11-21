<script setup lang="ts">
// The administrator's user filter: "" is the caller, "*" everyone, otherwise one email (the API's Scope).
defineProps<{ emails: string[]; me: string }>();
const user = defineModel<string>({ required: true });
</script>

<template>
  <select v-model="user" aria-label="User">
    <option value="">Me ({{ me }})</option>
    <option value="*">All users</option>
    <option v-for="e in emails" :key="e" :value="e">{{ e }}</option>
  </select>
</template>
