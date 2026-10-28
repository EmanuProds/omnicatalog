# Plan: OmniRoute Model Validator for OpenCode

## Overview
A **Go** program executed as a **systemd service** on Linux that:
1. Queries OmniRoute API to discover configured providers and their models
2. Filters **free** models with **tool calling** support
3. Validates functionality via **quick test (direct API)** + **real test (opencode run)**
4. Measures **speed/latency** of models
5. Stores validated catalog in **SQLite**
6. Updates **OpenCode agents/combos** (opencode.json) based on model capabilities

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    omnicatalog (Go binary)                      │
├─────────────────────────────────────────────────────────────────┤
│  CLI (cobra)                                                    │
│  ├── run           # Executes full validation (manual/cron)     │
│  ├── daemon        # systemd service mode (resident)            │
│  ├── status        # Shows last run status                      │
│  ├── models        # Lists models in local catalog              │
│  └── combos        # Shows/updates combos in opencode.json     │
├─────────────────────────────────────────────────────────────────┤
│  Core Services                                                  │
│  ├── OmniRouteClient    # HTTP client for OmniRoute API         │
│  ├── ModelValidator     # Direct API + opencode validation      │
│  ├── SpeedTester        # Latency/throughput benchmark          │
│  ├── CatalogStore       # SQLite (models, providers, combos)    │
│  ├── ComboManager       # Updates opencode.json agents          │
│  └── Scheduler          # Scheduling (daily/weekly/custom)      │
└─────────────────────────────────────────────────────────────────┘
```

---

## 1. Provider & Model Discovery (OmniRoute API)

### Endpoints Used
| Endpoint | Purpose |
|----------|---------|
| `GET /api/providers` | Lists configured providers (with connection status) |
| `GET /api/v1/models` | Lists all models available in configured providers |
| `POST /api/v1/models/query` | Filters models by capability (tool_calling, vision, etc) |
| `GET /api/combos` | Lists existing combos in OmniRoute (reference) |
| `GET /api/models/catalog` | Full catalog with metadata (pricing, capabilities) |

### Initial Filters (before testing)
- **Free**: `pricing.input == 0 && pricing.output == 0` OR provider marked as "free tier" in catalog
- **Tool Calling**: `capabilities.tool_calling == true` OR `supportsTools == true`
- **Exclude**: deprecated models, duplicate aliases, reasoning-only models without tools

### Providers with Explicit Free Tier (No Free Test Needed)
These **DO NOT need free tier testing**, only tool calling + speed + context:
- Providers with `free: true` in catalog
- Models with `pricing: { input: 0, output: 0 }` confirmed
- Known examples: Groq (free models), Cerebras, some Google/Gemini, "keyless" providers

### Providers Needing Free Tier Validation
- OpenRouter (rotating "free" models)
- Together AI (limited free tier)
- Fireworks AI
- Any provider without explicit pricing in catalog

### Free Tier Validation (Non-Explicit Providers)
For providers that **don't** explicitly indicate free tier:
- **30s window per provider**: Test models from SAME provider sequentially within 30s
- **Parallelism across providers**: Models from DIFFERENT providers can be validated simultaneously
- **Method**: Real API request + billing/usage verification (headers, response metadata, or quota endpoint)
- **Cache**: Free tier result cached 24h per model

---

## 2. Tool Calling Validation

### Phase 1: Quick Test (Direct API) - ~2-5s per model
```json
POST /v1/chat/completions
{
  "model": "provider/model-id",
  "messages": [{"role": "user", "content": "Use a tool to list files in current directory"}],
  "tools": [{
    "type": "function",
    "function": {
      "name": "list_files",
      "description": "List files in a directory",
      "parameters": {
        "type": "object",
        "properties": {
          "path": {"type": "string", "description": "Directory path"}
        },
        "required": ["path"]
      }
    }
  }],
  "tool_choice": "auto",
  "max_tokens": 100
}
```
**Success Criteria**: Response contains `tool_calls` array with `name: "list_files"` and valid arguments.

### Phase 2: Real Test (opencode run) - ~15-30s per model
```bash
# Creates task requiring tool usage
opencode run --model "provider/model-id" \
  "List all .go files in the current directory using the glob tool, then read the first one using the read tool"
```
**Success Criteria**:
- Subagent executes `glob` and `read` tools
- Returns coherent result
- No "model does not support tool calling" errors

### Optimization
- Parallelize tests (max 3-5 concurrent to avoid API saturation)
- Cache results (SQLite) - don't re-test models validated in last 24h
- Timeout: 30s direct API, 60s opencode run

---

## 3. Speed Test (Benchmark)

### Metrics Collected
| Metric | Description | Acceptable Target |
|--------|-------------|-------------------|
| `ttft_ms` | Time To First Token (streaming) | < 3000ms |
| `tps` | Tokens Per Second (sustained) | > 20 tps |
| `total_ms` | Total latency simple request | < 10000ms |
| `tool_latency_ms` | Tool call latency | < 5000ms |

### Methodology
1. **Warm-up**: 1 discarded request (loads model at provider)
2. **TTFT**: 3 streaming requests, measure time to first chunk
3. **Throughput**: 1 request 500 tokens, measure tokens/second
4. **Tool Calling**: 1 request with tool, measure tool_call → tool_result latency

### Speed Classification
| Class | TTFT | TPS | Recommended Use |
|-------|------|-----|-----------------|
| **Ultra** | < 500ms | > 50 | code |
| **Fast** | 500-1500ms | 30-50 | review, contracts |
| **Normal** | 1500-3000ms | 20-30 | orchestrate, database |
| **Slow** | > 3000ms | < 20 | docs |
| **Rejected** | > 5000ms | < 10 | Fallback only |

---

## 4. SQLite Catalog (Schema)

```sql
-- Providers configured in OmniRoute
CREATE TABLE providers (
    id TEXT PRIMARY KEY,           -- ex: "anthropic", "openai", "groq"
    name TEXT NOT NULL,
    status TEXT NOT NULL,          -- "connected", "error", "disabled"
    free_tier BOOLEAN DEFAULT 0,   -- provider has known free tier
    last_checked DATETIME,
    metadata JSON                  -- complete provider config
);

-- Discovered models
CREATE TABLE models (
    id TEXT PRIMARY KEY,           -- ex: "anthropic/claude-sonnet-4-6"
    provider_id TEXT NOT NULL,
    name TEXT NOT NULL,
    context_window INTEGER,        -- max context in tokens (validated)
    max_output_tokens INTEGER,
    supports_tools BOOLEAN DEFAULT 0,
    supports_vision BOOLEAN DEFAULT 0,
    supports_reasoning BOOLEAN DEFAULT 0,
    pricing_input REAL,            -- USD per 1M tokens (0 = free)
    pricing_output REAL,
    is_free BOOLEAN DEFAULT 0,     -- confirmed free
    free_confirmed BOOLEAN DEFAULT 0, -- validated by test
    free_tested_at DATETIME,       -- when free tier was tested
    capabilities JSON,             -- complete capabilities from OmniRoute
    discovered_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (provider_id) REFERENCES providers(id)
);

-- Validation results
CREATE TABLE validations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    model_id TEXT NOT NULL,
    validation_type TEXT NOT NULL, -- "api_tool_call", "opencode_tool_call", "speed"
    status TEXT NOT NULL,          -- "passed", "failed", "timeout", "error"
    details JSON,                  -- latency, tokens, error, etc
    tested_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (model_id) REFERENCES models(id)
);

-- Speed benchmarks
CREATE TABLE benchmarks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    model_id TEXT NOT NULL,
    ttft_ms INTEGER,
    tps REAL,
    total_ms INTEGER,
    tool_latency_ms INTEGER,
    speed_class TEXT,              -- "ultra", "fast", "normal", "slow"
    benchmarked_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (model_id) REFERENCES models(id)
);

-- OpenCode combos managed
CREATE TABLE opencode_combos (
    id TEXT PRIMARY KEY,           -- ex: "orchestrate", "code", "review"
    name TEXT NOT NULL,
    description TEXT,
    target_model_ids JSON,         -- array of primary models ordered
    fallback_model_ids JSON,       -- array of fallbacks ordered
    required_capabilities JSON,    -- ["reasoning", "tool_calling", "large_context"]
    min_speed_class TEXT,          -- "fast", "normal", etc
    min_context_window INTEGER,    -- minimum context in tokens
    strategy TEXT DEFAULT 'auto',  -- OmniRoute routing strategy
    enabled BOOLEAN DEFAULT 1,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Scheduler run log
CREATE TABLE scheduler_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_type TEXT NOT NULL,        -- "manual", "scheduled", "triggered"
    status TEXT NOT NULL,          -- "running", "completed", "failed"
    models_tested INTEGER DEFAULT 0,
    models_passed INTEGER DEFAULT 0,
    models_failed INTEGER DEFAULT 0,
    duration_ms INTEGER,
    started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME
);
```

---

## 5. Combo Mapping → Selection Criteria

### 6 Combo Definitions (OpenCode Agents)

| Combo (Agent ID) | Primary Focus | Required Capabilities | Min Speed | Min Context | Ideal Model |
|------------------|---------------|----------------------|-----------|-------------|-------------|
| **orchestrate** | Reasoning, planning, architecture, delegation | `reasoning`, `tool_calling`, `large_context` (≥1M) | Normal | **1M tokens** | Strong "thinking" models: Claude Sonnet, GPT-5, Kimi K2, Nemotron 3 Ultra |
| **code** | Speed, modern code creation/update, minimal errors | `tool_calling`, `code_generation` | Ultra | **200k tokens** | Fast code models: GPT-4o-mini, Claude Haiku, Gemini Flash, DeepSeek Coder |
| **review** | Bugs, fix planning, code/architecture evaluation | `reasoning`, `tool_calling`, `code_analysis` | Fast | **1M tokens** | Similar to orchestrate, analysis-focused: GPT-5, Claude Opus, Kimi |
| **contracts** | API contracts (OpenAPI, Orval), frontend↔backend | `tool_calling`, `structured_output`, `schema_generation` | Fast | **200k tokens** | JSON/Schema specialists: GPT-4o, Claude Sonnet, Gemini Pro |
| **database** | Cloud DB, SQL, NoSQL, migrations, optimization | `tool_calling`, `sql_generation`, `code_generation` | Normal | **200k tokens** | Specialized: SQLCoder, CodeLlama, or strong generalists |
| **docs** | Read/create documentation, PRs, READMEs | `tool_calling`, `large_context`, `writing` | Slow | **1M tokens** | Strong writers: Claude Sonnet, GPT-4o, Gemini Pro |

### Assignment Strategy
1. **Primary Model**: Best model meeting ALL required criteria + min speed
2. **Fallbacks (3-5)**: Ordered by criteria proximity, speed, availability
3. **OmniRoute Auto-combo**: Optional - create "auto" combo in OmniRoute with selected models

### Example Generated opencode.json
```jsonc
{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "omniroute": {
      "options": {
        "baseURL": "http://localhost:20128/v1",
        "apiMode": "chat"
      }
    }
  },
  "agents": {
    "orchestrate": {
      "description": "Orchestration, planning, architecture, delegation (1M+ context)",
      "mode": "primary",
      "model": "omniroute/orchestrate",
      "system": "You are the orchestrator agent. Plan, architect, and delegate tasks to specialized subagents."
    },
    "code": {
      "description": "Modern code creation/update, high speed (200k+ context)",
      "mode": "primary",
      "model": "omniroute/code",
      "system": "You are a senior software engineer. Write clean, modern, testable code with minimal errors. Prioritize speed."
    },
    "review": {
      "description": "Code review, bug hunting, architecture evaluation (1M+ context)",
      "mode": "subagent",
      "model": "omniroute/review",
      "system": "You are a senior code reviewer. Find bugs, vulnerabilities, architecture issues and suggest concrete fixes."
    },
    "contracts": {
      "description": "API contracts (OpenAPI, Orval), frontend-backend integration (200k+ context)",
      "mode": "subagent",
      "model": "omniroute/contracts",
      "system": "You are an API contract specialist. Generate OpenAPI specs, validate contracts, create TypeScript types via Orval."
    },
    "database": {
      "description": "Cloud databases, SQL, NoSQL, migrations, optimization (200k+ context)",
      "mode": "subagent",
      "model": "omniroute/database",
      "system": "You are a DBA/data engineer. Write optimized SQL, design schemas, plan migrations, optimize queries."
    },
    "docs": {
      "description": "Documentation, PRs, READMEs, technical guides (1M+ context)",
      "mode": "subagent",
      "model": "omniroute/docs",
      "system": "You are a technical writer. Create clear documentation, complete READMEs, PR descriptions, architecture guides."
    }
  }
}
```

---

## 6. CLI Interface (Cobra)

### Main Commands
```bash
# Single execution (cron/manual)
omnicatalog run [--config /path/config.yaml] [--only-new] [--force-revalidate]

# systemd daemon
omnicatalog daemon [--config /path/config.yaml]

# Status and queries
omnicatalog status
omnicatalog models [--free] [--validated] [--provider groq] [--combo code]
omnicatalog combos [--show-current] [--diff]

# Configuration
omnicatalog config init          # Creates default config.yaml
omnicatalog config validate      # Validates current config
omnicatalog schedule set daily   # Sets schedule (daily/weekly/custom)
```

### Configuration (config.yaml)
```yaml
omniroute:
  base_url: "http://localhost:20128"
  api_key: "{env:OMNIROUTE_API_KEY}"
  timeout: 30s

opencode:
  config_path: "~/.config/opencode/opencode.json"  # GLOBAL
  backup_before_update: true

validation:
  api_tool_call_timeout: 30s
  opencode_test_timeout: 60s
  max_concurrent_tests: 3
  revalidate_after_hours: 24

speed_test:
  warmup_requests: 1
  ttft_samples: 3
  throughput_tokens: 500
  min_ttft_ms: 3000
  min_tps: 20
  tool_call_samples: 2

context_validation:
  enabled: true
  test_tokens: 100000        # Tests with 100k token request
  min_context_orchestrate: 1000000
  min_context_code: 200000
  min_context_review: 1000000
  min_context_contracts: 200000
  min_context_database: 200000
  min_context_docs: 1000000

free_validation:
  enabled: true
  window_per_provider_sec: 30
  max_concurrent_providers: 5
  test_method: "billing_check"  # "billing_check" | "quota_endpoint" | "usage_headers"
  cache_ttl_hours: 24

scheduler:
  enabled: true
  schedule: "daily"              # daily, weekly, custom (cron expression)
  custom_cron: ""                # e.g. "0 3 * * 1,3,5" (Mon/Wed/Fri 3am)
  run_on_startup: true

database:
  path: "~/.local/share/omnicatalog/catalog.db"
  migration_dir: "migrations"

logging:
  level: "info"                  # debug, info, warn, error
  format: "json"                 # json, text
  output: "stdout"               # stdout, file, both
  file_path: "/var/log/omnicatalog.log"
```

---

## 7. Systemd Service

### `/etc/systemd/system/omnicatalog.service`
```ini
[Unit]
Description=OmniRoute Model Catalog Validator for OpenCode
After=network-online.target omniroute.service
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
User=emanuel
Group=emanuel
WorkingDirectory=/home/emanuel/Projetos/AI/omnicatalog
ExecStart=/usr/local/bin/omnicatalog daemon --config /home/emanuel/.config/omnicatalog/config.yaml
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal
SyslogIdentifier=omnicatalog

# Resource limits (minimum RAM)
MemoryLimit=256M
MemorySwapMax=0
CPUQuota=50%

# Security
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=/home/emanuel/.local/share/omnicatalog /home/emanuel/.config/omnicatalog /var/log/omnicatalog.log

[Install]
WantedBy=multi-user.target
```

### Installation
```bash
sudo cp omnicatalog /usr/local/bin/
sudo cp omnicatalog.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now omnicatalog
```

---

## 8. Main Execution Flow (`run`)

```mermaid
flowchart TD
    A[Start] --> B[Load Config]
    B --> C[Connect OmniRoute]
    C --> D[List Connected Providers]
    D --> E[For each Provider: Fetch Models]
    E --> F[Filter: Tool Calling + Min Context]
    F --> G{Provider declares\nfree tier explicitly?}
    G -->|Yes| H[Mark is_free=true, free_confirmed=true]
    G -->|No| I[Free Validation: 30s/provider\nReal request + billing/quota check]
    H --> J[Direct API Tool Calling Test]
    I --> J
    J --> K{Passed API?}
    K -->|No| L[Mark Failed - Next]
    K -->|Yes| M[Real opencode Tool Calling Test]
    M --> N{Passed OpenCode?}
    N -->|No| L
    N -->|Yes| O[Validate Max Context]
    O --> P[Speed Benchmark]
    P --> Q[Classify Speed Class]
    Q --> R[Save to SQLite]
    R --> S[Assign to 6 Combos by Criteria]
    S --> T[Create/Update Combos in OmniRoute API]
    T --> U[Generate Global opencode.json (combo refs only)]
    U --> V[Backup + Atomic Write]
    V --> W[End]
```

---

## 9. Go Dependencies (go.mod)

```go
module omnicatalog

go 1.23

require (
    github.com/spf13/cobra v1.8.0
    github.com/spf13/viper v1.19.0
    github.com/mattn/go-sqlite3 v1.14.0
    github.com/jmoiron/sqlx v1.3.0
    golang.org/x/sync v0.7.0
    golang.org/x/time v0.6.0
    gopkg.in/yaml.v3 v3.0.1
    github.com/rs/zerolog v1.32.0
    github.com/robfig/cron/v3 v3.0.1
    github.com/shirou/gopsutil/v3 v3.21.0
)
```

---

## 10. Acceptance Criteria (Definition of Done)

### Functional
- [ ] `omnicatalog run` executes full validation and updates OmniRoute combos + global opencode.json
- [ ] `omnicatalog daemon` runs as stable systemd service (< 100MB RAM)
- [ ] Dynamic discovery of providers configured in OmniRoute
- [ ] Correct filter: only models with tool_calling + min context
- [ ] Free tier validation: 30s/provider (sequential same provider, parallel different providers)
- [ ] Direct API tool call test validates in < 5s/model
- [ ] opencode run test validates real tool calling in < 60s/model
- [ ] Real max context validation per model
- [ ] Benchmark measures TTFT, TPS, tool latency
- [ ] Speed classification (ultra/fast/normal/slow) works
- [ ] 6 combos created/updated in OmniRoute via API (orchestrate, code, review, contracts, database, docs)
- [ ] Combos respect: min speed + min context per combo
- [ ] Global opencode.json generated referencing ONLY combos (omniroute/*)
- [ ] opencode-omniroute-auth plugin used for model discovery
- [ ] CLI scheduling (daily/weekly/custom cron) works

### Non-Functional
- [ ] Memory < 100MB idle, < 256M peak
- [ ] CPU < 10% average, peaks < 50% during validation
- [ ] Error handling: network, timeout, API changes
- [ ] Structured logs (JSON) for observability
- [ ] Versioned SQLite migrations
- [ ] Config via YAML + env vars + file secrets
- [ ] Unit tests > 80% coverage core services
- [ ] Reproducible build (Go modules)

---

## 11. Implementation Order

| Phase | Deliverable | Estimate |
|-------|-------------|----------|
| **1. Foundation** | Go project + Cobra CLI + Config (Viper) + SQLite migrations | 2-3 days |
| **2. OmniRoute Client** | HTTP client with auth, providers, models, combos endpoints | 2 days |
| **3. Model Discovery** | Sync providers/models → SQLite, initial filters (tool_calling + context) | 1-2 days |
| **4. Free Validation** | Free tier test 30s/provider (sequential same provider, parallel different providers) | 2 days |
| **5. Validation Engine** | API tool call test + opencode run test (parallel, timeouts) | 3-4 days |
| **6. Context Validation** | Real max context validation per model | 1-2 days |
| **7. Speed Benchmark** | TTFT, TPS, tool latency measurement + classification | 2 days |
| **8. Combo Manager** | Creates/updates 6 combos in OmniRoute via API + generates global opencode.json | 2-3 days |
| **9. Scheduler & Daemon** | Cron parser, systemd integration, lifecycle management | 2 days |
| **10. Polish** | Logs, metrics, tests, docs, install script | 2-3 days |
| **Total** | **~19-25 days** | |

---

## 12. Risks & Mitigations

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|------------|
| OmniRoute API changes (breaking) | Medium | High | Version detection, contract tests, graceful fallback |
| Provider removes free tier | High | Medium | Periodic re-validation, `free_confirmed` flag with 24h TTL |
| opencode run intermittent failures | Medium | Medium | Retry with backoff, distinguish model error vs infra error |
| Too many models to test | High | High | Aggressive pre-filter (tool_calling + context), controlled parallelism, 24h cache |
| Systemd service crash loop | Low | High | Health checks, circuit breaker, max restart limit |
| Global opencode.json conflicts | Medium | Medium | Auto backup, strategic merge, dry-run flag |
| Free validation 30s/provider fails | Medium | Medium | Per-model timeout, fallback to "assume paid", detailed logging |
| Real context differs from catalog | Medium | High | Empirical test required, cache result, weekly re-validation |
| OmniRoute combo create/update fails | Low | High | Idempotency, retry with backoff, post-write verification |

---

## 13. Technical References

- **OmniRoute API**: `skills/omni-providers`, `skills/omni-models`, `skills/omni-combos-routing`
- **OmniRoute Models Endpoint**: `GET /api/v1/models`, `POST /api/v1/models/query`
- **OpenCode Config**: `https://opencode.ai/config.json` schema
- **OpenCode Agents**: `https://opencode.ai/docs/agents`
- **OpenCode CLI**: `opencode run` for headless execution
- **opencode-omniroute-auth**: Plugin reference for model fetching