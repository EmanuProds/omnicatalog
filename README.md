# OmniCatalog

OmniRoute Model Validator for OpenCode — Discovers, validates, and catalogs **free** models with **tool calling** from OmniRoute, automatically updating **combos** in OpenCode.

## Objective

Runs as a **systemd service** in background (minimal RAM), OmniCatalog:

1. **Discovers** providers and models configured in OmniRoute via API
2. **Filters** free models with tool calling support
3. **Validates** real functionality:
   - Quick test: Direct API (`/v1/chat/completions` with tools)
   - Real test: `opencode run` with subagent using tools
4. **Validates** maximum context empirically per model
5. **Benchmarks** speed (TTFT, TPS, tool latency) → classification (ultra/fast/normal/slow/rejected)
6. **Creates/Updates** 6 combos in OmniRoute via API
7. **Generates** global `opencode.json` referencing only combos (`omniroute/orchestrate`, `omniroute/code`, etc.)

---

## Managed Combos (6 OpenCode Agents)

| Combo (Agent) | Min Speed | Min Context | Focus |
|---------------|-----------|-------------|-------|
| `orchestrate` | Normal | **1M** | Reasoning, planning, architecture, delegation |
| `code` | **Ultra** | **200k** | Speed, modern code, minimal errors |
| `review` | Fast | **1M** | Bugs, fixes, architecture evaluation |
| `contracts` | Fast | **200k** | OpenAPI, Orval, frontend↔backend |
| `database` | Normal | **200k** | Cloud DB, SQL, NoSQL, migrations |
| `docs` | Slow | **1M** | Documentation, PRs, READMEs |

> OpenCode only sees combos via `opencode-omniroute-auth` plugin — automatic fallback, RTK+Caveman compression, quota-aware routing.

---

## Requirements

- **Linux** with systemd
- **Go 1.23+** (for build)
- **OmniRoute** running at `http://localhost:20128` (or configurable)
- **OpenCode** installed and configured
- **Plugin** `opencode-omniroute-auth` in OpenCode

---

## Installation

### Build from source
```bash
git clone <repo>
cd omnicatalog
make build-prod
```

### Install as systemd service
```bash
sudo make install
# Or manually:
sudo cp build/omnicatalog /usr/local/bin/
sudo cp deploy/omnicatalog.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now omnicatalog
```

### Configuration
Copy `configs/config.yaml.example` to one of:
```bash
# System (high priority)
sudo cp configs/config.yaml.example /etc/omnicatalog/config.yaml

# User (medium priority)
mkdir -p ~/.config/omnicatalog
cp configs/config.yaml.example ~/.config/omnicatalog/config.yaml
```

Edit configuration:
```yaml
omniroute:
  base_url: "http://localhost:20128"
  api_key: "{env:OMNIROUTE_API_KEY}"  # Or {file:~/.config/omnicatalog/api_key}
  timeout: 30s

opencode:
  config_path: "~/.config/opencode/opencode.json"  # GLOBAL
  backup_before_update: true

# ... other settings (validation, speed_test, context_validation, free_validation, scheduler, database, logging)
```

Set OmniRoute API key:
```bash
# Via environment variable
export OMNIROUTE_API_KEY="sk-..."

# Or via file (more secure)
echo "sk-..." > ~/.config/omnicatalog/api_key
chmod 600 ~/.config/omnicatalog/api_key
```

---

## Usage

### CLI Commands
```bash
# Single run (manual/cron)
omnicatalog run [--only-new] [--force-revalidate] [--dry-run]

# Daemon (systemd)
omnicatalog daemon

# Status and queries
omnicatalog status
omnicatalog models [--free] [--validated] [--provider groq]
omnicatalog combos [--show-current] [--diff]

# Configuration
omnicatalog config validate
omnicatalog config init

# Scheduling
omnicatalog schedule set daily|weekly|custom
omnicatalog schedule show

# Version
omnicatalog version
```

### Scheduling
Configure via CLI or `config.yaml`:
```yaml
scheduler:
  enabled: true
  schedule: "daily"        # daily, weekly, custom
  custom_cron: ""          # e.g. "0 3 * * 1,3,5" (Mon/Wed/Fri 3am)
  run_on_startup: true
```

### Logs
```bash
# Via journalctl (systemd)
journalctl -u omnicatalog -f

# Or file (if configured)
tail -f /var/log/omnicatalog.log
```

---

## Development

### Structure
```
├── cmd/omnicatalog/main.go          # Entry point
├── internal/
│   ├── config/                      # Configuration (Viper)
│   ├── store/                       # SQLite (sqlx)
│   └── cli/commands.go              # CLI (Cobra)
├── migrations/001_initial_schema.sql
├── configs/config.yaml.example
├── deploy/omnicatalog.service
├── Makefile
├── go.mod
├── go.sum
└── PLAN.md                          # Detailed plan
```

### Makefile Targets
```bash
make build          # Dev build
make build-prod     # Production optimized build
make test           # Unit tests
make test-coverage  # HTML coverage
make fmt            # Format code
make vet            # go vet
make check          # fmt + vet + test
make install        # Install systemd (sudo)
make uninstall      # Remove systemd (sudo)
make clean          # Clean artifacts
make deps           # go mod tidy
```

### Tests
```bash
go test ./internal/... -v
go test ./internal/... -coverprofile=coverage.out && go tool cover -html=coverage.out
```



---

## Security

- Systemd service with hardening: `NoNewPrivileges`, `PrivateTmp`, `ProtectSystem=strict`, `ProtectHome=read-only`
- Secrets via `{file:...}` or `{env:...}` — never hardcoded
- OmniRoute API key only at runtime
- Local SQLite, no exposed network

---

## License

MIT