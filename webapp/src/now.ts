import { onBeforeUnmount, onMounted, ref, type Ref } from "vue";

// The page shares one clock for relative times: a single interval ticks once a minute while any component
// uses it and stops when the last one unmounts, so relative times refresh without a refetch.
const minuteMs = 60_000;
const now = ref(Date.now());
let users = 0;
let timer: ReturnType<typeof setInterval> | null = null;

/** useNow returns the shared clock in epoch milliseconds, ticking once a minute while mounted. */
export function useNow(): Readonly<Ref<number>> {
  // The clock may be up to a minute old; refresh it before a new user's first render rather than a tick later.
  now.value = Date.now();
  onMounted(() => {
    if (users++ > 0) return;
    timer = setInterval(() => (now.value = Date.now()), minuteMs);
  });
  onBeforeUnmount(() => {
    if (--users > 0 || !timer) return;
    clearInterval(timer);
    timer = null;
  });
  return now;
}
