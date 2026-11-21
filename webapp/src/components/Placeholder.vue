<script setup lang="ts">
// Stands in for a tab's view until it is built.
import { useRoute } from "vue-router";

const route = useRoute();
</script>

<template>
  <section class="card">
    <h2>{{ route.meta.title }}</h2>
  </section>
</template>
