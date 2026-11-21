<script setup lang="ts">
// The Wrench logo: an open-jaw wrench drawn in currentColor; the parent colors it (--accent in the brand).
// public/favicon.svg repeats the paths with the dark accent written out, since a favicon cannot read CSS.
withDefaults(defineProps<{ size?: number }>(), { size: 22 });
</script>

<template>
  <svg
    class="logo"
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="3"
    stroke-linecap="round"
    aria-hidden="true"
  >
    <path d="M14.6 4.5A4.5 4.5 0 1 0 19.5 9.4M11.8 12.2 4.5 19.5" />
  </svg>
</template>
