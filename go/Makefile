# =============================================================================
#  Fundus Marketplace – Makefile
#  Voraussetzungen: Go 1.22+, PowerShell (für deploy)
# =============================================================================

APP      := fundus-node
PKG      := github.com/fundus/node/cmd/fundus-node
BIN_DIR  := bin
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS  := -ldflags "-s -w -X main.Version=$(VERSION)"

# Pi-Ziel (ARMv7 = Pi 2/3/4/Zero 2, ARM64 = Pi 4/5 mit 64-Bit-OS)
PI_ARCH  ?= arm        # arm | arm64
PI_HOST  ?= 192.168.1.100
PI_USER  ?= pi

# =============================================================================
.PHONY: all build build-pi build-pi64 clean test lint fmt zip deploy

all: build

# -----------------------------------------------------------------------------
#  Lokaler Build (Entwicklungsmaschine)
# -----------------------------------------------------------------------------
build:
	@mkdir -p $(BIN_DIR)
	go build $(LDFLAGS) -o $(BIN_DIR)/$(APP) ./cmd/fundus-node
	@echo "  Built: $(BIN_DIR)/$(APP)"

# -----------------------------------------------------------------------------
#  Cross-Compile für Raspberry Pi (ARMv7 – Pi 3 / Pi 2 / Zero 2)
# -----------------------------------------------------------------------------
build-pi:
	@mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=arm GOARM=7 \
	  go build $(LDFLAGS) -o $(BIN_DIR)/$(APP)-armv7 ./cmd/fundus-node
	@echo "  Built: $(BIN_DIR)/$(APP)-armv7  (ARMv7, Pi 3/4/Zero2)"

# -----------------------------------------------------------------------------
#  Cross-Compile für Raspberry Pi 4/5 mit 64-Bit-Raspbian
# -----------------------------------------------------------------------------
build-pi64:
	@mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=arm64 \
	  go build $(LDFLAGS) -o $(BIN_DIR)/$(APP)-arm64 ./cmd/fundus-node
	@echo "  Built: $(BIN_DIR)/$(APP)-arm64  (ARM64, Pi 4/5)"

# -----------------------------------------------------------------------------
#  Tests
# -----------------------------------------------------------------------------
test:
	go test ./... -timeout 60s -race -count=1

test-short:
	go test ./... -timeout 30s -short -count=1

# Integrations-Tests (starten echten Server, dauern länger)
test-integration:
	go test ./test/integration/... -v -timeout 60s -count=1

# Alle Tests inklusive Integration
test-all: test test-integration

# Einzelnes Paket testen – Beispiel: make test-pkg PKG=./internal/geo
test-pkg:
	go test $(PKG) -v -timeout 30s -race

# Nur Tests deren Name ein Muster enthält – Beispiel: make test-run RUN=TestGridFee
test-run:
	go test ./... -run $(RUN) -v -timeout 30s

# Coverage-Report im Browser öffnen
cover:
	go test ./... -coverprofile=coverage.out -timeout 60s
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# Paket-Übersicht mit Prozentzahlen
cover-summary:
	go test ./... -coverprofile=coverage.out -timeout 60s
	go tool cover -func=coverage.out | tail -1

# -----------------------------------------------------------------------------
#  Lint / Format
# -----------------------------------------------------------------------------
lint:
	@which golangci-lint > /dev/null 2>&1 || \
	  (echo "  Installing golangci-lint…" && \
	   go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
	golangci-lint run ./...

fmt:
	gofmt -w .
	goimports -w . 2>/dev/null || true

# -----------------------------------------------------------------------------
#  ZIP für Deploy packen
#  Legt fundus.zip an das für das PowerShell-Script erwartet wird.
#  Enthält: Pi-Binary + Lua-Frontend + Service-Files + Config-Template
# -----------------------------------------------------------------------------
zip: build-pi
	@echo "  Packaging fundus.zip…"
	@mkdir -p dist/fundus/bin
	@mkdir -p dist/fundus/lua
	@mkdir -p dist/fundus/data

	# Binary
	cp $(BIN_DIR)/$(APP)-armv7  dist/fundus/bin/$(APP)
	chmod +x dist/fundus/bin/$(APP)

	# Lua Frontend
	cp -r lua/  dist/fundus/lua/

	# System-Configs
	cp fundus-node.service  dist/fundus/
	cp fundus-logrotate     dist/fundus/
	cp lua/fundus-nginx.conf dist/fundus/

	# Env-Vorlage (wird nur installiert wenn noch nicht vorhanden)
	cp fundus.env           dist/fundus/fundus.env.example

	cd dist && zip -r ../fundus.zip fundus/ -x "*.DS_Store" -x "__MACOSX/*"
	rm -rf dist
	@echo "  Ready: fundus.zip  ($(shell du -sh fundus.zip 2>/dev/null | cut -f1))"

zip64: build-pi64
	@echo "  Packaging fundus-arm64.zip…"
	@mkdir -p dist/fundus/bin
	cp $(BIN_DIR)/$(APP)-arm64 dist/fundus/bin/$(APP)
	chmod +x dist/fundus/bin/$(APP)
	cp -r lua/ dist/fundus/lua/
	cp fundus-node.service fundus-logrotate lua/fundus-nginx.conf fundus.env dist/fundus/
	cd dist && zip -r ../fundus-arm64.zip fundus/ -x "*.DS_Store"
	rm -rf dist
	@echo "  Ready: fundus-arm64.zip"

# -----------------------------------------------------------------------------
#  Direkt-Deploy auf den Pi (ohne PowerShell, für Linux/macOS-Entwickler)
# -----------------------------------------------------------------------------
deploy: zip
	@echo "  Deploying to $(PI_USER)@$(PI_HOST)…"
	scp fundus.zip $(PI_USER)@$(PI_HOST):/tmp/fundus-deploy.zip
	ssh $(PI_USER)@$(PI_HOST) '\
	  sudo systemctl stop fundus-node 2>/dev/null || true; \
	  sudo mkdir -p /opt/fundus; \
	  sudo unzip -o /tmp/fundus-deploy.zip -d /opt/fundus; \
	  sudo cp /opt/fundus/fundus-node.service /etc/systemd/system/; \
	  sudo systemctl daemon-reload; \
	  sudo systemctl enable fundus-node; \
	  sudo systemctl start fundus-node; \
	  rm -f /tmp/fundus-deploy.zip; \
	  echo "Done – status:"; \
	  systemctl is-active fundus-node \
	'

# -----------------------------------------------------------------------------
#  Docker
# -----------------------------------------------------------------------------

# Lokales Dev-Setup starten
docker-up:
	cd .. && docker compose up --build

# Mit LLM (Ollama)
docker-up-llm:
	cd .. && docker compose --profile llm up --build

# Mit zweitem Node für P2P-Tests
docker-up-p2p:
	cd .. && docker compose --profile p2p up --build

docker-down:
	cd .. && docker compose down

# Pi-Image bauen (ARMv7)
docker-build-pi:
	cd .. && docker build --platform linux/arm/v7 -t fundus-node:armv7 .

docker-build-pi64:
	cd .. && docker build --platform linux/arm64 -t fundus-node:arm64 .

# -----------------------------------------------------------------------------
#  Aufräumen
# -----------------------------------------------------------------------------
clean:
	rm -rf $(BIN_DIR) dist fundus.zip fundus-arm64.zip

# -----------------------------------------------------------------------------
#  Entwicklungs-Shortcuts
# -----------------------------------------------------------------------------

# Lokalen Node starten (Mock-Meter, kein Ollama nötig)
run:
	FUNDUS_METER_PROTOCOL=mock \
	FUNDUS_LLM_ENABLED=false \
	FUNDUS_DATA_DIR=/tmp/fundus-dev \
	go run ./cmd/fundus-node

# Mit Ollama + Mock-Meter (LLM-Tests)
run-llm:
	FUNDUS_METER_PROTOCOL=mock \
	FUNDUS_LLM_ENABLED=true \
	FUNDUS_DATA_DIR=/tmp/fundus-dev \
	go run ./cmd/fundus-node

# Logs vom Pi live streamen
logs:
	ssh $(PI_USER)@$(PI_HOST) "sudo journalctl -u fundus-node -f --no-pager"

# Pi-Status abfragen
status:
	ssh $(PI_USER)@$(PI_HOST) "systemctl status fundus-node"
	@echo ""
	curl -sf http://$(PI_HOST):80/api/v1/status | python3 -m json.tool 2>/dev/null || true
