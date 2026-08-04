# resumed — build, test, and run.
#
# Paths match the binary's own defaults, so `make serve` works on a fresh
# checkout. Point them somewhere else on the command line or in the
# environment when you keep your fact base elsewhere:
#
#   make serve DB=../resumed.db OUT=../jobs
#
BIN     := bin/resumed
DB      ?= $(HOME)/.resumed/resumed.db
OUT     ?= $(HOME)/.resumed/jobs
ADDR    ?= 127.0.0.1:7777
GOFILES := $(shell find . -name '*.go' -not -path './bin/*')

.DEFAULT_GOAL := help

## help: list the available targets
help:
	@echo "resumed targets:"
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | awk -F': ' '{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
	@echo
	@echo "variables: DB=$(DB)"
	@echo "           OUT=$(OUT)"
	@echo "           ADDR=$(ADDR)"

## build: compile the binary to bin/resumed
build: $(BIN)

$(BIN): $(GOFILES) go.mod go.sum
	go build -o $(BIN) ./cmd/resumed

## serve: run the web UI (Ctrl-C to stop)
serve: build
	@host=$(firstword $(subst :, ,$(ADDR))); port=$(lastword $(subst :, ,$(ADDR))); \
	if [ "$$host" = "0.0.0.0" ] || [ "$$host" = "::" ]; then \
		lan=$$(ipconfig getifaddr en0 2>/dev/null || hostname -I 2>/dev/null | awk '{print $$1}'); \
		echo "serving on all interfaces:"; \
		echo "  http://localhost:$$port"; \
		[ -n "$$lan" ] && echo "  http://$$lan:$$port   (reachable from your network)"; \
		echo; \
		echo "NOTE: the UI has no authentication and shows your contact details,"; \
		echo "      employment history, and every application you are tracking."; \
		echo "      Bind to 127.0.0.1 unless you actually need another device."; \
	else \
		echo "serving http://$(ADDR)"; \
	fi; \
	echo "db=$(DB)"; echo "out=$(OUT)"
	$(BIN) serve --db $(DB) --out $(OUT) --addr $(ADDR)

## serve-lan: serve on all interfaces, reachable from other devices
serve-lan:
	@$(MAKE) --no-print-directory serve ADDR=0.0.0.0:$(lastword $(subst :, ,$(ADDR)))

## mcp: run the MCP server over stdio (Claude launches this itself)
mcp: build
	$(BIN) mcp --db $(DB) --out $(OUT)

## install: register the MCP server with Claude for this project
install: build
	$(BIN) install --db $(DB) --out $(OUT)

## test: run the test suite
test:
	go test ./...

## check: gofmt, vet, and test — run this before committing
check:
	@unformatted=$$(gofmt -l cmd internal); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	go vet ./...
	go test ./...

## fmt: rewrite source with gofmt
fmt:
	gofmt -w cmd internal

## clean: remove build output
clean:
	rm -rf bin

.PHONY: help build serve serve-lan mcp install test check fmt clean
