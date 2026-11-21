<script setup lang="ts">
// The page opencode's device login opens: the signed-in user approves or denies the code shown in opencode.
// A signed-out visitor is sent to the GitLab sign-in, which returns here.
import { Check, X } from "lucide-vue-next";
import { onMounted, ref } from "vue";
import { ApiError, type Client, type Identity } from "../api";

const props = defineProps<{ client: Client; code: string; navigate: (url: string) => void }>();

const me = ref<Identity | null>(null);
const outcome = ref("");
const error = ref("");
const busy = ref(false);

onMounted(async () => {
  try {
    me.value = await props.client.me();
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) {
      const back = `/device?code=${props.code}`;
      props.navigate(`/auth/gitlab/login?return=${encodeURIComponent(back)}`);
      return;
    }
    error.value = e instanceof Error ? e.message : String(e);
  }
});

async function decide(decision: "approve" | "deny") {
  busy.value = true;
  error.value = "";
  try {
    await props.client.decideDevice(props.code, decision);
    outcome.value =
      decision === "approve"
        ? "Device approved. Return to opencode; it signs in by itself."
        : "Device login denied.";
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <section class="device">
    <h2>Sign in opencode</h2>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <template v-if="me && !outcome">
      <p>
        opencode asks to sign in as <strong>{{ me.email }}</strong
        >. Approve only if this code is the one opencode shows:
      </p>
      <p class="code mono">{{ code }}</p>
      <button type="button" class="approve primary" :disabled="busy" @click="decide('approve')">
        <Check :size="14" aria-hidden="true" />Approve
      </button>
      <button type="button" class="deny" :disabled="busy" @click="decide('deny')">
        <X :size="14" aria-hidden="true" />Deny
      </button>
    </template>
    <p v-if="outcome" role="status">{{ outcome }}</p>
  </section>
</template>
