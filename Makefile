# web/static/css/app.css is a checked-in, pre-built Tailwind v4 bundle —
# there's no Node.js build step in this repo (Phase 2 step 11's
# deliberate choice: standalone Tailwind CLI, nothing at runtime). That
# means it drifts silently: a template can start using a utility class
# that was never in app.css because nobody re-ran the build after adding
# it, and the class just quietly does nothing in the browser. This
# formalizes the rebuild so `make css` (or `make css-check` in CI) can
# catch that class of bug instead of relying on someone noticing.

TAILWIND_VERSION := v4.3.3
TAILWIND_BIN := .tools/tailwindcss-$(TAILWIND_VERSION)

TAILWIND_OS := $(shell uname -s | tr '[:upper:]' '[:lower:]' | sed 's/darwin/macos/')
TAILWIND_ARCH := $(shell uname -m | sed 's/x86_64/x64/;s/aarch64/arm64/')

.PHONY: css css-check

$(TAILWIND_BIN):
	mkdir -p .tools
	curl -sL -o $(TAILWIND_BIN) \
		"https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/tailwindcss-$(TAILWIND_OS)-$(TAILWIND_ARCH)"
	chmod +x $(TAILWIND_BIN)

css: $(TAILWIND_BIN) ## Rebuild web/static/css/app.css from input.css + the templates it scans.
	$(TAILWIND_BIN) --input web/static/css/input.css --output web/static/css/app.css --minify

css-check: css ## Rebuild, then fail if that changed the checked-in app.css (CI drift check).
	git diff --exit-code web/static/css/app.css
