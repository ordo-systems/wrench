import { ref } from "vue";

export type Theme = "dark" | "light";

const key = "wrench.theme";

/** theme is the active theme, for components that show the toggle. */
export const theme = ref<Theme>("dark");

// Storage can be blocked (private windows, site settings), so every access tolerates failure.
function stored(): Theme | null {
  try {
    const v = localStorage.getItem(key);
    return v === "dark" || v === "light" ? v : null;
  } catch {
    return null;
  }
}

function apply(t: Theme) {
  theme.value = t;
  document.documentElement.dataset.theme = t;
}

/** initTheme applies the remembered theme; the first visit follows prefers-color-scheme, dark by default. */
export function initTheme() {
  const light = typeof matchMedia === "function" && !matchMedia("(prefers-color-scheme: dark)").matches;
  apply(stored() ?? (light ? "light" : "dark"));
}

/** toggleTheme switches the theme and remembers the choice. */
export function toggleTheme() {
  const next: Theme = theme.value === "dark" ? "light" : "dark";
  apply(next);
  try {
    localStorage.setItem(key, next);
  } catch {
    // the choice still applies for this visit
  }
}
