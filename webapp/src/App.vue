<script setup lang="ts">
// The dashboard shell: loads the caller's identity, status and (for administrators) the open-alert count,
// keeps them fresh from the live stream, and hosts the routed tab views. /device renders without the shell.
import { computed, onBeforeUnmount, onMounted, provide, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import AlertBanner from "./components/AlertBanner.vue";
import DeviceApproval from "./components/DeviceApproval.vue";
import GitLabIcon from "./components/GitLabIcon.vue";
import Logo from "./components/Logo.vue";
import TopBar, { type Tab } from "./components/TopBar.vue";
import { ApiError, Client, clientKey, identityKey, type Identity, type Status } from "./api";
import { Live, liveKey } from "./live";
import { allowed, landing } from "./router";

const props = defineProps<{ fetchFn?: typeof fetch; live?: Live }>();

const route = useRoute();
const router = useRouter();
const client = new Client(props.fetchFn);
const live = props.live ?? new Live(() => new EventSource("/api/events"));
provide(liveKey, live);
provide(clientKey, client);

const error = ref("");
const loading = ref(true);
const me = ref<Identity | null>(null);
provide(identityKey, me);
const status = ref<Status | null>(null);
const openAlerts = ref(0);
const isAdmin = computed(() => me.value?.role === "admin");
const bare = computed(() => route.meta.bare === true);

const allTabs: (Tab & { admin?: boolean })[] = [
  { path: "/overview", label: "Overview", admin: true },
  { path: "/usage", label: "Usage" },
  { path: "/sessions", label: "Sessions" },
  { path: "/keys", label: "Keys" },
  { path: "/users", label: "Users", admin: true },
  { path: "/alerts", label: "Alerts", admin: true },
];
const tabs = computed<Tab[]>(() =>
  allTabs
    .filter((t) => isAdmin.value || !t.admin)
    .map((t) => (t.path === "/alerts" ? { ...t, count: openAlerts.value } : t)),
);

const showError = (e: unknown) => (error.value = e instanceof Error ? e.message : String(e));

async function loadMe() {
  try {
    me.value = await client.me();
  } catch (e) {
    me.value = null;
    if (!(e instanceof ApiError && e.status === 401)) showError(e);
  }
}

// The banner source; a failure keeps the last banner rather than hiding a critical incident.
async function loadStatus() {
  try {
    status.value = await client.status();
    error.value = "";
  } catch (e) {
    showError(e);
  }
}

// The count on the Alerts tab is the number of open incidents in the administrator's alert list.
async function loadAlerts() {
  if (!isAdmin.value) return;
  try {
    openAlerts.value = (await client.alerts("*")).alerts.filter((a) => a.resolved_at === null).length;
    error.value = "";
  } catch (e) {
    showError(e);
  }
}

async function load() {
  await loadMe();
  loading.value = false;
  if (me.value) await Promise.all([loadStatus(), loadAlerts()]);
}

// The template renders a route only when the role may open it, so a forbidden view never mounts or fetches
// before this redirect lands. Send the caller to a tab their role may open: "/" goes to the landing tab, admin routes redirect users.
watch(
  [me, () => route.path],
  () => {
    if (!me.value || bare.value) return;
    if (route.path === "/" || !allowed(route.path, me.value.role))
      void router.replace(landing(me.value.role));
  },
  { immediate: true },
);

// A stream the browser closed may mean the session is gone; only a 401 from /api/me says so, other errors
// (Wrench restarting) keep the dashboard and let the stream reopen.
async function checkSession() {
  try {
    await client.me();
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) endSession();
  }
}

const offs: (() => void)[] = [];
// Live handlers are registered once signed in, so the stream is never requested without a session.
watch(me, (v, old) => {
  if (!v || old || bare.value) return;
  offs.push(live.on("status", loadStatus), live.on("alerts", loadAlerts), live.onDropped(checkSession));
});

// endSession stops the live stream and shows the signed-out state.
function endSession() {
  offs.splice(0).forEach((off) => off());
  live.close();
  me.value = null;
  status.value = null;
}

async function signOut() {
  try {
    await client.logout();
  } catch {
    // the cookie is cleared server-side on success only; reload shows the real state
  }
  endSession();
}

const goTo = (url: string) => window.location.assign(url);
const deviceCode = computed(() => String(route.query.code ?? ""));

onMounted(() => void load());
onBeforeUnmount(() => {
  offs.forEach((off) => off());
  live.close();
});
</script>

<template>
  <DeviceApproval v-if="bare" class="page" :client="client" :code="deviceCode" :navigate="goTo" />

  <template v-else>
    <TopBar v-if="me" :me="me" :tabs="tabs" @sign-out="signOut" />
    <AlertBanner v-if="me && status" :alerts="status.alerts" :admin="isAdmin" />
    <main>
      <p v-if="error && me" class="error" role="alert">{{ error }}</p>

      <section v-if="!me && !loading" class="signin">
        <div class="card">
          <Logo :size="48" />
          <h1>Wrench</h1>
          <p class="muted">Private team coding agent</p>
          <a class="button primary" href="/auth/gitlab/login"><GitLabIcon />Sign in with GitLab</a>
          <p v-if="error" class="error" role="alert">{{ error }}</p>
        </div>
      </section>

      <RouterView v-if="me && allowed(route.path, me.role)" />
    </main>
  </template>
</template>

<style>
@import "./styles/tokens.css";
@import "./styles/utilities.css";
@import "./styles/buttons.css";

main,
.page {
  padding: 16px 24px;
  max-width: 1600px;
  margin: 0 auto;
  box-sizing: border-box;
}
/* Sign-in: a centered card on the page background. */
.signin {
  display: grid;
  place-items: center;
  min-height: calc(100vh - 32px);
}
.signin .card {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  width: min(100%, 360px);
  padding: 32px 24px;
  box-sizing: border-box;
  text-align: center;
}
.signin .logo {
  color: var(--accent);
}
.signin h1 {
  margin: 0;
  font-size: 24px;
}
.signin p {
  margin: 0;
}
.signin .button {
  margin-top: 8px;
  padding: 8px 16px;
}
.error {
  color: var(--bad);
}
.muted {
  color: var(--muted);
}
.warn,
.warning {
  color: var(--warn);
  font-weight: 600;
}
.critical {
  color: var(--bad);
  font-weight: 600;
}

/* Layout shared by the tab views: a responsive grid of cards; .wide cards span the whole row. */
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 360px), 1fr));
  gap: 16px;
  align-items: start;
}
/* Grid items default to min-width: auto and grow to their widest table; 0 lets .scroll wrappers scroll. */
.grid > * {
  min-width: 0;
}
.grid > .wide {
  grid-column: 1 / -1;
}
.card h2 {
  margin: 0 0 12px;
  font-size: 16px;
}
.pill {
  display: inline-block;
  padding: 1px 8px;
  border: 1px solid var(--line);
  font-size: 12px;
}
.pill.ok {
  border-color: var(--ok);
  color: var(--ok);
}
.pill.warn {
  border-color: var(--warn);
  color: var(--warn);
}
.pill.bad {
  border-color: var(--bad);
  color: var(--bad);
}
.pill.muted {
  border-color: var(--line);
  color: var(--muted);
}
.scroll {
  min-width: 0;
  overflow-x: auto;
}
table {
  border-collapse: collapse;
  font-size: 14px;
  width: 100%;
}
th,
td {
  text-align: left;
  padding: 4px 8px;
  border-bottom: 1px solid var(--line);
  white-space: nowrap;
}
th {
  color: var(--muted);
  font-weight: 500;
}
</style>
