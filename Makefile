BINARY  := splunk-mcp
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X github.com/nlink-jp/splunk-mcp/cmd.Version=$(VERSION)"
DIST_DIR := dist

.PHONY: build build-all test vet check clean \
	splunk-up splunk-down integration-test

build:
	@mkdir -p $(DIST_DIR)
	go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY) .

build-all:
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY)-linux-amd64   .
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY)-linux-arm64   .
	# darwin is arm64-only (no amd64, no universal — see org CONVENTIONS.md §Release Archive Standard)
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY)-darwin-arm64  .
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY)-windows-amd64.exe .

test:
	go test ./...

vet:
	go vet ./...

check: vet test build

splunk-up:
	@eval "$$(scripts/splunk-up.sh)" && \
		printf '\nSplunk is up. To set env vars in your shell:\n' && \
		printf '  eval "$$(scripts/splunk-up.sh)"\n\n'

splunk-down:
	scripts/splunk-down.sh

## Run integration tests against a live Splunk container.
## Starts Splunk automatically if not already running; leaves it running afterwards.
## Use 'make splunk-down' to tear it down when done.
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

clean:
	rm -rf $(DIST_DIR)
