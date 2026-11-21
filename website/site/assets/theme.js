// Theme of the website, as in the dashboard: the remembered choice, otherwise prefers-color-scheme, dark by default.
// Loaded in <head> without defer, so the theme is set before the page paints.
(function () {
  var key = "wrench.theme";
  var root = document.documentElement;

  // Storage can be blocked (private windows, site settings), so every access tolerates failure.
  function stored() {
    try {
      var v = localStorage.getItem(key);
      return v === "dark" || v === "light" ? v : null;
    } catch (e) {
      return null;
    }
  }

  var light = window.matchMedia && !window.matchMedia("(prefers-color-scheme: dark)").matches;
  root.dataset.theme = stored() || (light ? "light" : "dark");

  function label(button) {
    var next = root.dataset.theme === "dark" ? "light" : "dark";
    button.setAttribute("aria-label", "Switch to " + next + " theme");
    button.querySelector(".sun").style.display = root.dataset.theme === "dark" ? "" : "none";
    button.querySelector(".moon").style.display = root.dataset.theme === "dark" ? "none" : "";
  }

  document.addEventListener("DOMContentLoaded", function () {
    var button = document.querySelector("[data-theme-toggle]");
    if (!button) return;
    label(button);
    button.addEventListener("click", function () {
      root.dataset.theme = root.dataset.theme === "dark" ? "light" : "dark";
      try {
        localStorage.setItem(key, root.dataset.theme);
      } catch (e) {
        // the choice still applies on this page
      }
      label(button);
    });
  });
})();
