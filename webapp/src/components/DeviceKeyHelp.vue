<script setup lang="ts">
// How to get a new device key: install opencode, run the device login against this server, approve the code.
import { KeyRound } from "lucide-vue-next";
import CopyButton from "./CopyButton.vue";
import InfoDialog from "./InfoDialog.vue";
import { deviceLoginCommand } from "../format";

const open = defineModel<boolean>("open", { required: true });
const command = deviceLoginCommand(location.origin);
</script>

<template>
  <InfoDialog v-model:open="open" title="New device key" :icon="KeyRound" wide>
    <ol class="steps">
      <li class="step">
        <span class="step-num">1</span>
        <div>Install opencode on the device.</div>
      </li>
      <li class="step">
        <span class="step-num">2</span>
        <div class="step-body">
          Run this command on the device:
          <div class="command-block">
            <div class="command-bar">
              <span class="muted">Terminal</span>
              <CopyButton :text="command" />
            </div>
            <code>{{ command }}</code>
          </div>
        </div>
      </li>
      <li class="step">
        <span class="step-num">3</span>
        <div>Open the <code>/device</code> link it prints, check that the code matches and approve.</div>
      </li>
    </ol>
  </InfoDialog>
</template>

<style scoped>
.steps {
  display: grid;
  gap: 16px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.step {
  display: grid;
  grid-template-columns: 24px minmax(0, 1fr);
  gap: 12px;
  align-items: start;
  line-height: 24px;
}
.step-num {
  display: inline-grid;
  place-items: center;
  width: 24px;
  height: 24px;
  border-radius: 50%;
  border: 1px solid var(--accent);
  color: var(--accent);
  font-size: 12px;
  font-weight: 600;
}
.command-block {
  margin-top: 8px;
  background: var(--page);
  border: 1px solid var(--line);
  border-radius: 8px;
  overflow: hidden;
}
/* The bar above the command holds Copy, so it stays in place however long the command is. */
.command-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 4px 8px 4px 12px;
  border-bottom: 1px solid var(--line);
  font-size: 12px;
  line-height: 20px;
}
.command-block code {
  display: block;
  padding: 10px 12px;
  overflow-x: auto;
  white-space: pre;
  line-height: 20px;
}
</style>
