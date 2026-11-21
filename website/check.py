"""Checks the static site in site/: every local link and asset exists, every #anchor exists in its page, and the
documentation pages share one header and sidebar. Standard library only; exits 1 with one line per problem."""

import sys
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import urlsplit

SITE = Path(__file__).resolve().parent / "site"


class Page(HTMLParser):
    """Collects a page's links, ids, and the markup of its header and documentation sidebar."""

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.links, self.ids = [], set()
        self.shared, self._depth, self._capture = [], 0, None

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if "id" in a:
            self.ids.add(a["id"])
        for key in ("href", "src"):
            if a.get(key):
                self.links.append((a[key], self.getpos()[0]))
        if self._capture is None and tag in ("header", "aside"):
            self._capture, self._depth = tag, 0
        if self._capture:
            if tag == self._capture:
                self._depth += 1
            # aria-current marks the page itself; it differs by design.
            kept = sorted((k, v) for k, v in attrs if k != "aria-current")
            self.shared.append((tag, tuple(kept)))

    def handle_endtag(self, tag):
        if self._capture and tag == self._capture:
            self._depth -= 1
            if self._depth == 0:
                self._capture = None

    def handle_data(self, data):
        if self._capture and data.strip():
            self.shared.append(("#text", data.strip()))


def parse(path):
    page = Page()
    page.feed(path.read_text(encoding="utf-8"))
    return page


def target(page_path, url):
    """The file a local URL points at, or None for external URLs."""
    parts = urlsplit(url)
    if parts.scheme or parts.netloc or url.startswith("mailto:"):
        return None, None
    path = page_path if not parts.path else (page_path.parent / parts.path).resolve()
    if parts.path.endswith("/") or path.is_dir():
        path = path / "index.html"
    return path, parts.fragment


def main():
    pages = {p: parse(p) for p in sorted(SITE.rglob("*.html"))}
    problems = []
    for path, page in pages.items():
        rel = path.relative_to(SITE)
        for url, line in page.links:
            dest, fragment = target(path, url)
            if dest is None:
                continue
            if not dest.is_file() or SITE not in dest.parents:
                problems.append(f"{rel}:{line}: {url}: no such file")
            elif fragment and dest in pages and fragment not in pages[dest].ids:
                problems.append(f"{rel}:{line}: {url}: no #{fragment} in {dest.relative_to(SITE)}")
    docs = {p: pg for p, pg in pages.items() if p.parent == SITE / "docs"}
    if docs:
        first, reference = next(iter(docs.items()))
        for path, page in docs.items():
            if page.shared != reference.shared:
                problems.append(
                    f"{path.relative_to(SITE)}: header or sidebar differs from {first.relative_to(SITE)}"
                )
    for p in problems:
        print(p)
    print(f"{len(pages)} pages, {sum(len(p.links) for p in pages.values())} links, {len(problems)} problems")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
