# Dupearr — build, test and release.
#
#   make            web UI + binary (./bin/dupearr)
#   make build      binary only (embeds whatever is in web/dist)
#   make help       list targets

BINARY  := dupearr
MODULE  := github.com/sl0wz3r/dupearr
GO      ?= go

VERSION ?= $(shell v=$$(git describe --tags --dirty 2>/dev/null); echo $${v:-0.1.0-dev} | sed 's/^v//')
COMMIT  ?= $(shell git rev-parse --short --verify -q HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(MODULE)/internal/version.BuildDate=$(DATE)

EXE := $(if $(filter windows,$(shell $(GO) env GOOS)),.exe,)

# `make run` data directory (ignored by git).
DATA_DIR ?= $(CURDIR)/tmp/data

RELEASE_PLATFORMS ?= linux/amd64 linux/arm64 linux/arm/v7 darwin/amd64 darwin/arm64 windows/amd64

# Container image (Dockerfile at the repository root).
#   make docker        builds for this machine's architecture and loads it into Docker
#   make docker-push   builds DOCKER_PLATFORMS and pushes to REGISTRY (run `docker login` first)
# docker-push uses a dedicated buildx builder (docker-container driver): the default "docker"
# driver cannot push multi-arch images everywhere, and BuildKit ignores the Docker daemon's
# insecure-registries, so a plain-HTTP registry (the LAN Gitea) must be configured in BuildKit.
IMAGE            ?= dupearr
REGISTRY         ?= ghcr.io/sl0wz3r/dupearr
REGISTRY_HOST    := $(firstword $(subst /, ,$(REGISTRY)))
# true = the registry speaks plain HTTP (today: the LAN Gitea without TLS). Never assumed: over
# plain HTTP the registry login and the pushed image cross the network unprotected, so anyone on
# the path can read the token or swap the image. Opt in explicitly, on a trusted network only:
#   make docker-push REGISTRY_HTTP=true
REGISTRY_HTTP    ?= false
DOCKER_PLATFORM  ?= linux/$(shell docker version --format '{{.Server.Arch}}' 2>/dev/null || echo amd64)
DOCKER_PLATFORMS ?= linux/amd64,linux/arm64
# One builder per transport: a builder keeps the BuildKit config it was created with, so a builder
# made for plain HTTP (or the "dupearr-builder" of earlier versions, which assumed plain HTTP for the
# LAN registry) must never be reused for a push that did not opt in to it.
DOCKER_BUILDER   ?= dupearr-builder-$(if $(filter true,$(REGISTRY_HTTP)),plainhttp,https)
# Also tag/push :latest, the tag the Unraid template and the compose file pull. Defaults to true
# only for a release version (plain X.Y.Z, i.e. a clean vX.Y.Z tag); dev, dirty, pre-release and
# ad-hoc versions need an explicit DOCKER_LATEST=true (the release workflow never moves :latest
# for them either).
DOCKER_LATEST    ?= $(shell printf '%s\n' '$(VERSION)' | grep -Eqx '[0-9]+\.[0-9]+\.[0-9]+' && echo true || echo false)
DOCKER_FLAGS     ?=
DOCKER_BUILD_ARGS = --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(DATE)

# Registry-free install (an Unraid server that cannot or should not pull from the registry):
#   make docker-save        builds DOCKER_SAVE_PLATFORM (default linux/amd64: any x86-64 server) as
#                           IMAGE:VERSION-<arch> and writes DOCKER_SAVE_FILE (+ .sha256) for `docker load`
#   make docker-test-amd64  builds IMAGE:VERSION-amd64 and runs docker/test-image.sh on it (through
#                           emulation on an arm64 host such as Apple Silicon)
# Both only ever create the "-<arch>" tag: never IMAGE:VERSION or IMAGE:latest, so building for another
# architecture never replaces the image this machine runs. Install steps: unraid/README.md.
DOCKER_SAVE_PLATFORM ?= linux/amd64
# linux/amd64 -> amd64, linux/arm64 -> arm64, linux/arm/v7 -> armv7 (same names as `make release`)
DOCKER_SAVE_ARCH  = $(subst /,,$(patsubst linux/%,%,$(DOCKER_SAVE_PLATFORM)))
DOCKER_SAVE_TAG   = $(IMAGE):$(VERSION)-$(DOCKER_SAVE_ARCH)
DOCKER_SAVE_FILE ?= dist/$(BINARY)_$(VERSION)_linux-$(DOCKER_SAVE_ARCH).tar.gz
# docker/test-image.sh: run the image as this platform (docker run --platform). Empty: the image's own
# platform whenever it differs from the Docker daemon's (the script detects that itself).
PLATFORM ?=

# docker_build_for PLATFORM,TAG: build one platform and load it into Docker under TAG only.
# --provenance=false keeps it a single plain image (no attestation manifest), so `docker save` writes
# an archive that older Docker Engines load as well (Unraid 6.12.x ships Docker 20.10 to 24.0).
docker_build_for = docker buildx build --platform $(1) --load --provenance=false $(DOCKER_BUILD_ARGS) -t $(2) $(DOCKER_FLAGS) .
# docker_check_platform PLATFORM,TAG: fail unless the loaded image TAG really is PLATFORM (a builder
# without that platform, or an emulation setup, must not produce a mislabelled archive).
docker_check_platform = got=$$(docker image inspect --format '{{.Os}}/{{.Architecture}}{{with .Variant}}/{{.}}{{end}}' '$(2)') && \
	case "$$got" in '$(1)'|'$(1)'/*) echo "$(2) is $$got" ;; *) echo "$(2) is $$got, not $(1)" >&2; exit 1 ;; esac

.PHONY: all help web build run test e2e lint release docker docker-test docker-test-amd64 docker-save docker-builder docker-push clean

all: web build ## Build the web UI and the binary

help: ## List targets and the main variables (override with `make <target> VAR=value`)
	@echo "Targets:"
	@grep -E '^[a-z][a-z0-9-]*:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-17s %s\n", $$1, $$2}'
	@echo "Variables (current value):"
	@printf '  %-20s %s\n' \
		VERSION "$(VERSION)" \
		RELEASE_PLATFORMS "$(RELEASE_PLATFORMS)" \
		IMAGE "$(IMAGE)" \
		DOCKER_PLATFORM "$(DOCKER_PLATFORM)" \
		REGISTRY "$(REGISTRY)" \
		REGISTRY_HTTP "$(REGISTRY_HTTP)" \
		DOCKER_PLATFORMS "$(DOCKER_PLATFORMS)" \
		DOCKER_LATEST "$(DOCKER_LATEST)" \
		DOCKER_FLAGS "$(DOCKER_FLAGS)" \
		DOCKER_SAVE_PLATFORM "$(DOCKER_SAVE_PLATFORM)" \
		DOCKER_SAVE_FILE "$(DOCKER_SAVE_FILE)" \
		PLATFORM "$(if $(PLATFORM),$(PLATFORM),(image's own when it differs from the daemon's))"

web: ## Build the React UI into web/dist (embedded by go:embed)
	cd web && npm ci && npm run build
	@# vite empties dist/; restore the placeholder so Go-only builds keep compiling
	@touch web/dist/.gitkeep

build: ## Build ./bin/dupearr for this platform (static, CGO_ENABLED=0)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY)$(EXE) ./cmd/dupearr

run: build ## Build and run with a throwaway data dir (DATA_DIR, default ./tmp/data)
	./bin/$(BINARY)$(EXE) --data "$(DATA_DIR)" --nobrowser

test: ## Run all Go tests with the race detector
	$(GO) test ./... -race -count=1

e2e: ## End-to-end tests: the real binary vs fake Plex/*arr (DUPEARR_E2E_RACE=1 race-builds it)
	$(GO) test -tags e2e ./internal/e2e/... -count=1 -timeout 15m

lint: ## go vet + gofmt check
	$(GO) vet ./...
	@unformatted="$$(gofmt -l cmd internal tools web/embed.go)"; \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi

release: ## Cross-compile archives for RELEASE_PLATFORMS into dist/ (+ checksums.txt); run `make web` first
	@# The binaries embed web/dist: refuse to ship archives without the UI.
	@if [ ! -f web/dist/index.html ]; then \
		echo "web/dist/index.html is missing: run 'make web' first (release binaries embed the UI)" >&2; exit 1; \
	fi
	@rm -rf dist && mkdir -p dist
	@set -e; for p in $(RELEASE_PLATFORMS); do \
		os=$${p%%/*}; rest=$${p#*/}; arch=$${rest%%/*}; arm=""; \
		if [ "$$rest" != "$$arch" ]; then arm=$${rest#*/v}; fi; \
		name=$(BINARY)_$(VERSION)_$${os}_$${arch}$${arm:+v$$arm}; \
		ext=""; if [ "$$os" = windows ]; then ext=.exe; fi; \
		echo "==> $$name"; \
		mkdir -p dist/$$name; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch GOARM=$$arm \
			$(GO) build -trimpath -tags timetzdata -ldflags "$(LDFLAGS)" -o dist/$$name/$(BINARY)$$ext ./cmd/dupearr; \
		for f in LICENSE README.md CHANGELOG.md; do if [ -f $$f ]; then cp $$f dist/$$name/; fi; done; \
		if [ "$$os" = linux ] && [ -f deploy/systemd/dupearr.service ]; then cp deploy/systemd/dupearr.service dist/$$name/; fi; \
		if [ "$$os" = windows ]; then (cd dist && zip -qr $$name.zip $$name); \
		else tar -C dist -czf dist/$$name.tar.gz $$name; fi; \
		rm -rf dist/$$name; \
	done
	@cd dist && if command -v sha256sum >/dev/null; then sha256sum $(BINARY)_*; else shasum -a 256 $(BINARY)_*; fi > checksums.txt
	@ls -1 dist

docker: ## Build the image for this machine (DOCKER_PLATFORM) and load it as IMAGE:VERSION + IMAGE:latest
	docker buildx build --platform $(DOCKER_PLATFORM) --load $(DOCKER_BUILD_ARGS) \
		-t $(IMAGE):$(VERSION) -t $(IMAGE):latest $(DOCKER_FLAGS) .

docker-test: docker ## Build the image, then test entrypoint, exec wrapper, signals and a real server (docker/test-image.sh)
	PLATFORM='$(PLATFORM)' sh docker/test-image.sh $(IMAGE):$(VERSION)

docker-test-amd64: ## Build IMAGE:VERSION-amd64 (linux/amd64; emulated on arm64 hosts) and run docker/test-image.sh on it
	$(call docker_build_for,linux/amd64,$(IMAGE):$(VERSION)-amd64)
	@$(call docker_check_platform,linux/amd64,$(IMAGE):$(VERSION)-amd64)
	PLATFORM=linux/amd64 sh docker/test-image.sh $(IMAGE):$(VERSION)-amd64

docker-save: ## Build DOCKER_SAVE_PLATFORM as IMAGE:VERSION-<arch>; save it to DOCKER_SAVE_FILE (+ .sha256) for docker load
	@case '$(DOCKER_SAVE_PLATFORM)' in linux/?*) ;; *) \
		echo "DOCKER_SAVE_PLATFORM must be one Linux platform such as linux/amd64 (got '$(DOCKER_SAVE_PLATFORM)')" >&2; exit 1 ;; esac
	$(call docker_build_for,$(DOCKER_SAVE_PLATFORM),$(DOCKER_SAVE_TAG))
	@$(call docker_check_platform,$(DOCKER_SAVE_PLATFORM),$(DOCKER_SAVE_TAG))
	@# docker save | gzip, written to a temporary name first: a failed save (whose status the pipe
	@# would hide) or an archive without the tag never ends up under the final name.
	@set -e; \
	out='$(DOCKER_SAVE_FILE)'; dir=$$(dirname "$$out"); name=$$(basename "$$out"); tmp="$$out.tmp"; \
	mkdir -p "$$dir"; rm -f "$$tmp" "$$tmp.failed" "$$out.sha256"; \
	{ docker save '$(DOCKER_SAVE_TAG)' || : >"$$tmp.failed"; } | gzip -n >"$$tmp"; \
	if [ -e "$$tmp.failed" ]; then rm -f "$$tmp" "$$tmp.failed"; echo "docker save $(DOCKER_SAVE_TAG) failed" >&2; exit 1; fi; \
	if ! gzip -dc "$$tmp" | tar -xOf - manifest.json | grep -qF '$(DOCKER_SAVE_TAG)"'; then \
		rm -f "$$tmp"; echo "$$tmp: no manifest.json naming $(DOCKER_SAVE_TAG); not a docker load archive" >&2; exit 1; \
	fi; \
	mv -f "$$tmp" "$$out"; \
	cd "$$dir"; \
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$$name"; else shasum -a 256 "$$name"; fi >"$$name.sha256"; \
	echo "Wrote $$out ($$(du -h "$$name" | awk '{print $$1}')) and $$out.sha256 for $(DOCKER_SAVE_TAG) ($(DOCKER_SAVE_PLATFORM))"; \
	echo "On the server: sha256sum -c $$name.sha256 && docker load -i $$name   (unraid/README.md, registry-free install)"

docker-builder: ## Create the buildx builder used by docker-push (plain-HTTP aware, see REGISTRY_HTTP)
	@if docker buildx inspect $(DOCKER_BUILDER) >/dev/null 2>&1; then \
		echo "Using existing buildx builder $(DOCKER_BUILDER) (docker buildx rm $(DOCKER_BUILDER) to recreate it)"; \
	else \
		set -e; cfg=""; \
		if [ "$(REGISTRY_HTTP)" = true ]; then \
			mkdir -p tmp; \
			printf '[registry."%s"]\n  http = true\n' "$(REGISTRY_HOST)" > tmp/buildkitd.toml; \
			cfg="--buildkitd-config tmp/buildkitd.toml"; \
			echo "WARNING: BuildKit will use plain HTTP for $(REGISTRY_HOST): no encryption, no server authentication. Trusted networks only." >&2; \
		fi; \
		docker buildx create --name $(DOCKER_BUILDER) --driver docker-container $$cfg --bootstrap; \
	fi

docker-push: docker-builder ## Build DOCKER_PLATFORMS and push REGISTRY:VERSION (+ :latest, see DOCKER_LATEST); `docker login` first
	@if [ "$(REGISTRY_HTTP)" = true ]; then \
		echo "WARNING: pushing to $(REGISTRY_HOST) over plain HTTP (REGISTRY_HTTP=true): the login and the image are not protected in transit. Publish the digest below so users can pin it." >&2; \
	fi
	@mkdir -p tmp
	docker buildx build --builder $(DOCKER_BUILDER) --platform $(DOCKER_PLATFORMS) --push \
		--provenance=false --metadata-file tmp/docker-push.json $(DOCKER_BUILD_ARGS) \
		-t $(REGISTRY):$(VERSION) $(if $(filter true,$(DOCKER_LATEST)),-t $(REGISTRY):latest) $(DOCKER_FLAGS) .
	@digest=$$(sed -n 's/.*"containerimage.digest": *"\(sha256:[0-9a-f]*\)".*/\1/p' tmp/docker-push.json | head -n 1); \
		if [ -n "$$digest" ]; then echo "Pushed $(REGISTRY):$(VERSION)@$$digest (pin this digest)"; fi

TEST_ENV := docker compose -f deploy/test-env/docker-compose.yml

test-env: ## Build + start the local Docker test environment (Dupearr + fake Plex/*arrs) on :3873 and pre-configure it
	$(TEST_ENV) up -d --build
	sh deploy/test-env/setup.sh

test-env-reset: ## Restart the test environment from scratch (fresh Dupearr config + fresh fake library)
	$(TEST_ENV) down -v
	$(MAKE) test-env

test-env-logs: ## Follow the test environment's logs
	$(TEST_ENV) logs -f

test-env-down: ## Stop the test environment (keeps its volumes; test-env-reset wipes them)
	$(TEST_ENV) down

# One-command public demo (deploy/demo): Dupearr + the fake Plex/*arrs of the demo-media image
# (docker/fakemedia.Dockerfile), seeded through the API; users pull both images from the registry.
# demo-local builds both from this tree for this machine (DOCKER_PLATFORM), runs deploy/demo/test.sh
# with them as Compose project DEMO_PROJECT on 127.0.0.1:DEMO_PORT (seed, the default scenario's 10
# groups, dry run, recycle bin, restore, idempotent re-seed), then removes the demo, its volumes and
# both images. DEMO_KEEP=true keeps everything and leaves the demo running.
# Its image names are its own (not DEMO_DUPEARR_IMAGE / DEMO_MEDIA_IMAGE, which the compose file
# reads from the environment): demo-local builds these tags and deletes them afterwards.
DEMO_LOCAL_IMAGE       ?= dupearr-demo-local:dev
DEMO_LOCAL_MEDIA_IMAGE ?= dupearr-demo-media-local:dev
DEMO_PORT              ?= 38731
DEMO_PROJECT           ?= dupearr-demo-local
DEMO_KEEP              ?= false

.PHONY: demo-local
demo-local: ## Build Dupearr + demo-media from this tree, test deploy/demo on 127.0.0.1:DEMO_PORT, clean up
	docker buildx build --platform $(DOCKER_PLATFORM) --load $(DOCKER_BUILD_ARGS) -t $(DEMO_LOCAL_IMAGE) $(DOCKER_FLAGS) .
	docker buildx build --platform $(DOCKER_PLATFORM) --load $(DOCKER_BUILD_ARGS) -f docker/fakemedia.Dockerfile -t $(DEMO_LOCAL_MEDIA_IMAGE) .
	@status=0; \
	DEMO_DUPEARR_IMAGE='$(DEMO_LOCAL_IMAGE)' DEMO_MEDIA_IMAGE='$(DEMO_LOCAL_MEDIA_IMAGE)' DEMO_PORT='$(DEMO_PORT)' \
		DEMO_PROJECT='$(DEMO_PROJECT)' DEMO_KEEP='$(DEMO_KEEP)' sh deploy/demo/test.sh || status=$$?; \
	if [ '$(DEMO_KEEP)' = true ]; then \
		echo "DEMO_KEEP=true: kept $(DEMO_LOCAL_IMAGE) and $(DEMO_LOCAL_MEDIA_IMAGE)"; \
	else \
		docker image rm '$(DEMO_LOCAL_IMAGE)' '$(DEMO_LOCAL_MEDIA_IMAGE)' >/dev/null && echo "Removed $(DEMO_LOCAL_IMAGE) and $(DEMO_LOCAL_MEDIA_IMAGE)"; \
	fi; \
	exit $$status

# Unraid Community Applications (CA): the public template and ca_profile.xml are rendered from
# unraid/ca/*.tmpl with the values in ONE file, CA_ENV (unraid/ca/publish.env). Workflow and
# submission steps: unraid/ca/README.md and unraid/README.md. Rendered files are validated before
# they replace the committed ones; unraid/ca/out/ is scratch space (git-ignored).
CA_DIR         := unraid/ca
CA_OUT         := $(CA_DIR)/out
CA_ENV         ?= $(CA_DIR)/publish.env
CA_PRIVATE_ENV ?= $(CA_DIR)/private.env
CA_TEMPLATE    := unraid/dupearr.xml
CA_ICON        := unraid/icon.png
CA_PROFILE     ?= ca_profile.xml

.PHONY: ca-template ca-profile ca-private ca-validate ca-preflight ca-vars

ca-template: ## Render + validate unraid/dupearr.xml (public CA template) from unraid/ca/ and CA_ENV
	@mkdir -p $(CA_OUT)
	sh $(CA_DIR)/render.sh -e $(CA_ENV) -o $(CA_OUT)/dupearr.xml $(CA_DIR)/dupearr.xml.tmpl
	sh $(CA_DIR)/validate-template.sh --icon $(CA_ICON) --repo . --as $(CA_TEMPLATE) $(CA_OUT)/dupearr.xml
	cp $(CA_OUT)/dupearr.xml $(CA_TEMPLATE)
	@echo "Updated $(CA_TEMPLATE). Commit it together with the .tmpl/env change."

ca-profile: ## Render + validate the repository-root ca_profile.xml (CA_PROFILE) from unraid/ca/ and CA_ENV
	@mkdir -p $(CA_OUT)
	sh $(CA_DIR)/render.sh -e $(CA_ENV) -o $(CA_OUT)/ca_profile.xml $(CA_DIR)/ca_profile.xml.tmpl
	sh $(CA_DIR)/validate-template.sh --profile --as ca_profile.xml $(CA_OUT)/ca_profile.xml
	cp $(CA_OUT)/ca_profile.xml $(CA_PROFILE)
	@echo "Updated $(CA_PROFILE)."

ca-private: ## Render a LAN/private-registry template (CA_PRIVATE_ENV over CA_ENV) to unraid/ca/out/dupearr-private.xml; never commit it
	@test -f $(CA_PRIVATE_ENV) || { echo "$(CA_PRIVATE_ENV) not found: cp $(CA_DIR)/private.env.example $(CA_PRIVATE_ENV) and fill it in" >&2; exit 1; }
	@mkdir -p $(CA_OUT)
	sh $(CA_DIR)/render.sh -e $(CA_ENV) -e $(CA_PRIVATE_ENV) -o $(CA_OUT)/dupearr-private.xml $(CA_DIR)/dupearr.xml.tmpl
	sh $(CA_DIR)/validate-template.sh --private --icon $(CA_ICON) --as $(CA_TEMPLATE) $(CA_OUT)/dupearr-private.xml
	@echo "LAN template: $(CA_OUT)/dupearr-private.xml (install steps: unraid/README.md, Private LAN variant)"

ca-validate: ## Offline CA checks: committed template/profile match their sources and pass every rule
	@mkdir -p $(CA_OUT)
	@sh $(CA_DIR)/render.sh -e $(CA_ENV) -o $(CA_OUT)/dupearr.check.xml $(CA_DIR)/dupearr.xml.tmpl >/dev/null
	@cmp -s $(CA_OUT)/dupearr.check.xml $(CA_TEMPLATE) || { echo "$(CA_TEMPLATE) is out of date with $(CA_DIR)/dupearr.xml.tmpl + $(CA_ENV): run make ca-template" >&2; exit 1; }
	sh $(CA_DIR)/validate-template.sh --icon $(CA_ICON) --repo . $(CA_TEMPLATE)
	@if [ -f $(CA_PROFILE) ]; then \
		sh $(CA_DIR)/render.sh -e $(CA_ENV) -o $(CA_OUT)/ca_profile.check.xml $(CA_DIR)/ca_profile.xml.tmpl >/dev/null && \
		{ cmp -s $(CA_OUT)/ca_profile.check.xml $(CA_PROFILE) || { echo "$(CA_PROFILE) is out of date: run make ca-profile" >&2; exit 1; }; } && \
		sh $(CA_DIR)/validate-template.sh --profile --as ca_profile.xml $(CA_PROFILE); \
	else echo "WARN  $(CA_PROFILE) not rendered yet: run make ca-profile before the repository goes public"; fi

ca-preflight: ## Online, read-only, anonymous checks of the live public repo, raw URLs, icon and image (amd64/arm64)
	sh $(CA_DIR)/preflight.sh -e $(CA_ENV)

ca-vars: ## Print every public CA value derived from CA_ENV (URLs to paste into the submission form)
	@sh $(CA_DIR)/render.sh -e $(CA_ENV) --print


clean: ## Remove build output (bin/, dist/, tmp/, web/dist contents)
	rm -rf bin dist tmp coverage.out coverage.html
	@if [ -d web/dist ]; then find web/dist -mindepth 1 ! -name .gitkeep -exec rm -rf {} +; fi
