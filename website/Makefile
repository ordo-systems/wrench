# Wrench website: the static files in site/ are published to GitHub Pages as they are; nothing is built.

SHELL         := bash -euo pipefail
.DEFAULT_GOAL := help
MAKEFLAGS     += --no-builtin-rules --no-print-directory

PORT ?= 8000## Port serve listens on (127.0.0.1)

.PHONY: help serve open check

help: ## Show targets
	@awk 'BEGIN { FS = ":.*## " } /^[a-z-]+:.*## / { printf "  %-8s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@echo "  PORT=$(PORT)"

serve: ## Serve site/ on http://127.0.0.1:PORT
	@echo "http://127.0.0.1:$(PORT)/"
	python3 -m http.server $(PORT) --bind 127.0.0.1 --directory site

open: ## Serve site/ and open it in the browser
	@(sleep 1 && { open "http://127.0.0.1:$(PORT)/" 2>/dev/null || xdg-open "http://127.0.0.1:$(PORT)/"; }) &
	@$(MAKE) serve

check: ## Local links, anchors and the docs' shared header and sidebar
	python3 check.py
