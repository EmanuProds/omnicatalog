# Makefile para omnicatalog

.PHONY: build test lint fmt vet clean install uninstall run daemon status models combos config schedule version

# Variáveis
BINARY_NAME := omnicatalog
BUILD_DIR := ./build
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)"

# Default target
all: build

# Build do binário
build:
	@echo "🔨 Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	@go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/omnicatalog
	@echo "✅ Build completo: $(BUILD_DIR)/$(BINARY_NAME)"

# Build para produção (otimizado)
build-prod:
	@echo "🔨 Building $(BINARY_NAME) (produção)..."
	@mkdir -p $(BUILD_DIR)
	@CGO_ENABLED=1 go build -trimpath $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/omnicatalog
	@echo "✅ Build produção completo: $(BUILD_DIR)/$(BINARY_NAME)"

# Testes
test:
	@echo "🧪 Executando testes..."
	@go test -v -race -count=1 ./...

test-coverage:
	@echo "🧪 Executando testes com cobertura..."
	@go test -v -race -count=1 -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "📊 Cobertura gerada: coverage.html"

# Linting e formatação
lint:
	@echo "🔍 Executando linters..."
	@golangci-lint run ./... || echo "⚠️  golangci-lint não instalado, pulando..."

fmt:
	@echo "🎨 Formatando código..."
	@go fmt ./...
	@goimports -w .

vet:
	@echo "🔍 Executando go vet..."
	@go vet ./...

# Verificações completas
check: fmt vet test
	@echo "✅ Todas as verificações passaram"

# Execução
run: build
	@echo "🚀 Executando..."
	@$(BUILD_DIR)/$(BINARY_NAME) run

daemon: build
	@echo "🚀 Iniciando daemon..."
	@$(BUILD_DIR)/$(BINARY_NAME) daemon

status: build
	@$(BUILD_DIR)/$(BINARY_NAME) status

models: build
	@$(BUILD_DIR)/$(BINARY_NAME) models --validated

combos: build
	@$(BUILD_DIR)/$(BINARY_NAME) combos --show-current

config: build
	@$(BUILD_DIR)/$(BINARY_NAME) config validate

schedule: build
	@$(BUILD_DIR)/$(BINARY_NAME) schedule show

version: build
	@$(BUILD_DIR)/$(BINARY_NAME) version

# Instalação systemd
install: build-prod
	@echo "📦 Instalando $(BINARY_NAME)..."
	@sudo cp $(BUILD_DIR)/$(BINARY_NAME) /usr/local/bin/
	@sudo cp deploy/omnicatalog.service /etc/systemd/system/
	@sudo systemctl daemon-reload
	@sudo systemctl enable --now omnicatalog
	@echo "✅ Instalado e iniciado como systemd service"

uninstall:
	@echo "🗑️  Removendo $(BINARY_NAME)..."
	@sudo systemctl disable --now omnicatalog 2>/dev/null || true
	@sudo rm -f /etc/systemd/system/omnicatalog.service
	@sudo rm -f /usr/local/bin/$(BINARY_NAME)
	@sudo systemctl daemon-reload
	@echo "✅ Removido"

# Limpeza
clean:
	@echo "🧹 Limpando..."
	@rm -rf $(BUILD_DIR)
	@rm -f coverage.out coverage.html
	@go clean -cache -modcache -testcache

# Dependências
deps:
	@echo "📥 Baixando dependências..."
	@go mod download
	@go mod tidy
	@go mod verify

# Gera mocks para testes (requer mockgen)
mocks:
	@echo "🎭 Gerando mocks..."
	@go generate ./...

# Help
help:
	@echo "omnicatalog - Makefile targets:"
	@echo ""
	@echo "  build          - Compila o binário (dev)"
	@echo "  build-prod     - Compila otimizado para produção"
	@echo "  test           - Executa testes"
	@echo "  test-coverage  - Executa testes com cobertura HTML"
	@echo "  lint           - Executa golangci-lint"
	@echo "  fmt            - Formata código (go fmt + goimports)"
	@echo "  vet            - Executa go vet"
	@echo "  check          - fmt + vet + test"
	@echo "  run            - Build e executa 'run'"
	@echo "  daemon         - Build e executa 'daemon'"
	@echo "  status         - Build e executa 'status'"
	@echo "  models         - Build e lista modelos validados"
	@echo "  combos         - Build e mostra combos"
	@echo "  config         - Build e valida config"
	@echo "  schedule       - Build e mostra agendamento"
	@echo "  version        - Build e mostra versão"
	@echo "  install        - Instala como systemd service (requer sudo)"
	@echo "  uninstall      - Remove systemd service e binário"
	@echo "  clean          - Limpa artifacts de build"
	@echo "  deps           - Baixar e organizar dependências"
	@echo "  mocks          - Gera mocks para testes"
	@echo "  help           - Mostra esta ajuda"