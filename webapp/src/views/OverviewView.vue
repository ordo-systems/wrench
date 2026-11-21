<script setup lang="ts">
// Overview is the administrator's landing tab: a two-by-two grid of status, the shared budget, models and tiers,
// then everyone's usage.
import BudgetWidget from "../widgets/BudgetWidget.vue";
import ModelsWidget from "../widgets/ModelsWidget.vue";
import StatusWidget from "../widgets/StatusWidget.vue";
import TiersWidget from "../widgets/TiersWidget.vue";
import UsageWidget from "../widgets/UsageWidget.vue";
</script>

<template>
  <div class="grid">
    <div class="grid-2x2 wide">
      <StatusWidget admin />
      <BudgetWidget scope="global" />
      <ModelsWidget admin />
      <TiersWidget />
    </div>
    <UsageWidget class="wide" scope="system" />
  </div>
</template>
