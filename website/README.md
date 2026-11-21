# Wrench website

The GitHub Pages site: the landing page and short user documentation. The `pages` workflow publishes `site/` as it
is when `website/` changes on `master` (see [Releases](../docs/operations.md#releases)); nothing is built.

```sh
make serve    # http://127.0.0.1:8000 (PORT=…)
make open     # the same, opened in the browser
make check    # local links, #anchors, and the docs pages' shared header and sidebar
```

- `site/assets/site.css` repeats the dashboard's theme tokens, card, button and top bar (`webapp/src/styles`,
  `TopBar.vue`); keep them in step when the dashboard changes. `site/assets/theme.js` applies the same theme choice.
- The pages in `site/docs/` share one header and sidebar; `make check` fails when they drift apart.
- The pages summarize the full reference in the repository's `docs/`; when behavior changes, update both.
- `check.py` and this `Makefile` stay outside `site/`, so they are not published. The root `make lint` runs
  `make check`.
