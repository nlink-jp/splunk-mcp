MODULE  := github.com/nlink-jp/splunk-mcp
BINARY  := splunk-mcp
DIST_DIR := dist

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-s -w -X $(MODULE)/cmd.Version=$(VERSION)"

# macOS Developer ID signing / notarization (see CONVENTIONS.md §Code
# Signing). Defaults match any Developer ID Application cert in the
# keychain and the org-standard notary profile. Builds without these
# fall back to ad-hoc / un-notarized with a one-line warning.
CODESIGN_IDENTITY ?= Developer ID Application
NOTARY_PROFILE    ?= nlink-jp-notary

# darwin ships arm64 only (no amd64, no universal). linux/windows keep their matrix.
PLATFORMS := darwin/arm64 linux/amd64 linux/arm64 windows/amd64

.PHONY: build build-all package test vet check clean help \
	splunk-up splunk-down integration-test

## build: Build binary for the current OS/Arch → ./dist/splunk-mcp
build:
	@mkdir -p $(DIST_DIR)
	go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY) .
	@scripts/codesign-darwin.sh $(DIST_DIR)/$(BINARY) "$(CODESIGN_IDENTITY)"

## build-all: Cross-compile and codesign the darwin build
build-all:
	@mkdir -p $(DIST_DIR)
	@for p in $(PLATFORMS); do os=$${p%/*}; arch=$${p#*/}; \
		ext=""; [ "$$os" = windows ] && ext=".exe"; \
		echo "Building $(DIST_DIR)/$(BINARY)-$$os-$$arch$$ext..."; \
		GOOS=$$os GOARCH=$$arch go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY)-$$os-$$arch$$ext . ; \
	done
	@scripts/codesign-darwin.sh $(DIST_DIR)/$(BINARY)-darwin-arm64 "$(CODESIGN_IDENTITY)" "$(BINARY)"

## package: archive each platform as <name>-v<version>-<os>-<arch>.<ext>
## (darwin/windows=zip, linux=tar.gz); canonical binary + README + LICENSE
## inside; notarize the darwin arm64 zip.
package: build-all
	@cd $(DIST_DIR) && for p in $(PLATFORMS); do os=$${p%/*}; arch=$${p#*/}; \
		ext=""; [ "$$os" = windows ] && ext=".exe"; \
		stage=_pkg; rm -rf $$stage; mkdir -p $$stage; \
		cp "$(BINARY)-$$os-$$arch$$ext" "$$stage/$(BINARY)$$ext"; \
		cp ../README.md ../LICENSE $$stage/; \
		base="$(BINARY)-$(VERSION)-$$os-$$arch"; \
		if [ "$$os" = linux ]; then ( cd $$stage && tar -czf "../$$base.tar.gz" * ); \
		else ( cd $$stage && zip -q "../$$base.zip" * ); fi; \
		rm -rf $$stage; \
	done
	@scripts/notarize-darwin.sh $(DIST_DIR)/$(BINARY)-$(VERSION)-darwin-arm64.zip "$(NOTARY_PROFILE)"

## test: Run all unit tests
test:
	go test ./...

## vet: Run go vet
vet:
	go vet ./...

## check: vet + test + build
check: vet test build

## splunk-up: Start the Splunk integration-test container (Podman)
splunk-up:
	@eval "$$(scripts/splunk-up.sh)" && \
		printf '\nSplunk is up. To set env vars in your shell:\n' && \
		printf '  eval "$$(scripts/splunk-up.sh)"\n\n'

## splunk-down: Stop and remove the Splunk test container
splunk-down:
	scripts/splunk-down.sh

## integration-test: Run -tags integration tests against a live Splunk container.
## Starts Splunk automatically if not already running; leaves it running afterwards.
integration-test:
	@if ! podman container exists splunk-test 2>/dev/null || \
	    [ "$$(podman inspect --format '{{.State.Status}}' splunk-test 2>/dev/null)" != "running" ]; then \
		echo "[integration-test] Starting Splunk container..."; \
		eval "$$(scripts/splunk-up.sh)"; \
	else \
		echo "[integration-test] Container already running."; \
	fi
	@HOST=$$(podman port splunk-test 8089/tcp | cut -d: -f2) && \
		TOKEN=$$(curl -sk \
			-d "username=admin&password=Admin1234!&output_mode=json" \
			"https://localhost:$${HOST}/services/auth/login" \
			| python3 -c "import sys,json; print(json.load(sys.stdin)['sessionKey'])") && \
		SPLUNK_HOST="https://localhost:$${HOST}" \
		SPLUNK_TOKEN="$${TOKEN}" \
		go test -v -tags integration -timeout 10m ./internal/client/... ./internal/tools/...

## clean: Remove build artifacts
clean:
	rm -rf $(DIST_DIR)

## help: Show available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'

# Homebrew tap generation (see scripts/release-brew.mk). After `make package`,
# `make brew` generates this formula from the built darwin-arm64 zip into the
# local nlink-jp/homebrew-tap checkout. The package target is unchanged.
BREW_KIND := formula
BREW_DESC := MCP server for Splunk search with exact result counts over the REST API
include scripts/release-brew.mk
