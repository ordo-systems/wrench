<script setup lang="ts">
// A modal confirmation on the native <dialog>. Cancel, Escape and a backdrop click close it without
// confirming; Confirm emits "confirm" once and closes. The parent owns the open state (v-model:open).
import { Check, X } from "lucide-vue-next";
import { onMounted, ref, watch, type Component } from "vue";

// confirmIcon marks the confirm button (Check by default); Cancel always carries X.
withDefaults(
  defineProps<{ title: string; message: string; confirmLabel: string; confirmIcon?: Component }>(),
  {
    confirmIcon: () => Check,
  },
);
const emit = defineEmits<{ confirm: [] }>();
const open = defineModel<boolean>("open", { required: true });
const dialog = ref<HTMLDialogElement | null>(null);

function sync() {
  const d = dialog.value;
  if (!d || open.value === d.hasAttribute("open")) return;
  if (open.value) d.showModal();
  else d.close();
}
onMounted(sync);
watch(open, sync, { flush: "post" });

const dismiss = () => (open.value = false);

function confirm() {
  if (!open.value) return;
  open.value = false;
  emit("confirm");
}

// A click whose target is the dialog itself landed on the backdrop: the content sits in an inner element.
function onClick(e: MouseEvent) {
  if (e.target === dialog.value) dismiss();
}

// Escape fires "cancel"; closing goes through the model so the parent sees it. "close" covers other closes.
function onCancel(e: Event) {
  e.preventDefault();
  dismiss();
}
</script>

<template>
  <dialog ref="dialog" class="confirm" @click="onClick" @cancel="onCancel" @close="dismiss">
    <div class="body">
      <h2>{{ title }}</h2>
      <p>{{ message }}</p>
      <div class="actions">
        <button type="button" data-cancel @click="dismiss"><X :size="14" aria-hidden="true" />Cancel</button>
        <button type="button" class="danger" @click="confirm">
          <component :is="confirmIcon" :size="14" aria-hidden="true" />{{ confirmLabel }}
        </button>
      </div>
    </div>
  </dialog>
</template>

<style scoped>
.confirm {
  padding: 0;
  max-width: min(440px, calc(100vw - 32px));
  background: var(--card);
  color: var(--text);
  border: 1px solid var(--line);
  border-radius: 10px;
}
.confirm::backdrop {
  background: rgb(0 0 0 / 50%);
}
.body {
  padding: 20px;
}
h2 {
  margin: 0 0 8px;
  font-size: 16px;
}
p {
  margin: 0 0 20px;
  color: var(--muted);
  overflow-wrap: anywhere;
}
.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
