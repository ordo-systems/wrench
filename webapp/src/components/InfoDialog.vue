<script setup lang="ts">
// A modal information dialog on the native <dialog>: a title with an optional icon, slotted content and a Close
// button. Escape, Close and a backdrop click close it. The parent owns the open state (v-model:open); wide widens
// it for content such as a command line.
import { X } from "lucide-vue-next";
import { onMounted, ref, watch, type Component } from "vue";

defineProps<{ title: string; icon?: Component; wide?: boolean }>();
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

// A click whose target is the dialog itself landed on the backdrop: the content sits in an inner element.
function onClick(e: MouseEvent) {
  if (e.target === dialog.value) dismiss();
}

// Escape fires "cancel"; closing goes through the model so the parent sees it.
function onCancel(e: Event) {
  e.preventDefault();
  dismiss();
}
</script>

<template>
  <dialog
    ref="dialog"
    class="info"
    :class="{ wide }"
    :aria-label="title"
    @click="onClick"
    @cancel="onCancel"
    @close="dismiss"
  >
    <div class="body">
      <h2><component :is="icon" v-if="icon" :size="18" aria-hidden="true" />{{ title }}</h2>
      <slot />
      <div class="actions">
        <button type="button" class="primary" data-close @click="dismiss">
          <X :size="14" aria-hidden="true" />Close
        </button>
      </div>
    </div>
  </dialog>
</template>

<style scoped>
.info {
  padding: 0;
  width: 560px;
  max-width: calc(100vw - 32px);
  background: var(--card);
  color: var(--text);
  border: 1px solid var(--line);
  border-radius: 10px;
}
.info::backdrop {
  background: rgb(0 0 0 / 50%);
}
.body {
  padding: 20px;
}
.info.wide {
  width: 880px;
}
h2 {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0 0 16px;
  font-size: 16px;
}
.actions {
  display: flex;
  justify-content: flex-end;
  margin-top: 20px;
}
</style>
