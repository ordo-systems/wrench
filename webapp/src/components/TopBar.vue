<script setup lang="ts">
// The top bar: brand (a link to the start page), the tabs the role may open, the status indicator, the signed-in
// identity and the theme toggle.
import { LogOut, Moon, Sun } from "lucide-vue-next";
import type { Identity } from "../api";
import Logo from "./Logo.vue";
import StatusIndicator from "./StatusIndicator.vue";
import { theme, toggleTheme } from "../theme";

export interface Tab {
  path: string;
  label: string;
  count?: number;
}

defineProps<{ me: Identity; tabs: Tab[] }>();
defineEmits<{ signOut: [] }>();
</script>

<template>
  <header class="bar">
    <RouterLink to="/" class="brand"><Logo />Wrench</RouterLink>
    <nav aria-label="Dashboard">
      <RouterLink v-for="t in tabs" :key="t.path" :to="t.path">
        {{ t.label }}<span v-if="t.count" class="count">{{ t.count }}</span>
      </RouterLink>
    </nav>
    <StatusIndicator :admin="me.role === 'admin'" />
    <span class="who mono">{{ me.email }} ({{ me.role }})</span>
    <button
      type="button"
      class="icon"
      :aria-label="`Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`"
      @click="toggleTheme"
    >
      <Sun v-if="theme === 'dark'" :size="18" />
      <Moon v-else :size="18" />
    </button>
    <button type="button" @click="$emit('signOut')"><LogOut :size="14" aria-hidden="true" />Sign out</button>
  </header>
</template>

<style scoped>
.bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px 20px;
  padding: 0 24px;
  background: var(--card);
  border-bottom: 1px solid var(--line);
}
.brand {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: var(--text);
  text-decoration: none;
  font-weight: 700;
  font-size: 18px;
}
.brand .logo {
  color: var(--accent);
}
nav {
  display: flex;
  flex-wrap: wrap;
  flex: 1;
  gap: 4px;
}
nav a {
  padding: 16px 12px 12px;
  border-bottom: 4px solid transparent;
  color: var(--muted);
  text-decoration: none;
  font-weight: 500;
}
nav a.router-link-active {
  color: var(--text);
  border-bottom-color: var(--accent);
}
.count {
  margin-left: 6px;
  padding: 0 7px;
  border: 1px solid var(--bad);
  border-radius: 999px;
  color: var(--bad);
  font-size: 12px;
}
.who {
  color: var(--muted);
  font-size: 13px;
}
</style>
