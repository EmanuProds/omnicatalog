# Plan: OmniCatalog — Automated OmniRoute → OpenCode Setup

> **Status (v2, 2026-11-14)** — Rewritten for the stated purpose: **do automatically
> what the user used to do manually in OmniRoute + OpenCode.**
> The previous plan (discovery → tool/context/speed validation → combos) described a
> validator. v2 extends it into a full product: discover → classify free models →
> validate admitted free models → assemble combos by family catalog + quota rules →
> write combos to OmniRoute → own `opencode.json` completely, including installing and
> configuring `@omniroute/opencode-plugin-v2`. Old content (probe-based free
> validation, dry-run, manual multiselect) is in git history; `--dry-run` was removed.

---

## 1. Purpose & scope

OmniCatalog automates the whole path from a fresh OmniRoute install to a fully
wired OpenCode:

1. **What models exist?** — discovery via the OmniRoute API (providers, connections,
   models). Saved to SQLite: free ones (admitted) and paid ones (priced, read-only).
2. **Which are free?** — models OmniRoute flags as free/zero-cost, using the
   documented free-tier catalog (`/api/free-tier/summary`) + provider inference, and
   its documented signals only. **No probes are ever sent to paid models.**
3. **Are they real?** — only *admitted* free models are validated
   (tool calling → context window → speed benchmark).
4. **Which go where?** — admitted free models are assembled into the six combos
   according to per-combo family/model rules (a YAML catalog: which families/models
   belong to which combo, given strengths and weaknesses) plus quota rules (provider
   accounts → distinct models admitted).
5. **Are combos published to OpenCode?** — OmniCatalog writes the combos into
   OmniRoute and **owns the whole `opencode.json`** (provider block, agents, plugin).
6. **Does OpenCode see the combos?** — OmniCatalog verifies (and installs/configures
   if needed) `@omniroute/opencode-plugin-v2`, ensuring OpenCode lists exactly the
   combos.

Not in scope:

- Probing paid models (cost, TOS).
- Managing OpenCode sessions/state beyond `opencode.json`.
- Paid-model validation (they are cataloged only).
- `omnicatalog run --dry-run` (removed).

---

## 2. Execution flow (`omnicatalog run`)

```
PHASE 1  Discovery   providers + connections (accounts, quota signals) + models
PHASE 2  Classify    documented free catalog per sync; persist models (free+paid)
PHASE 3  Validate    admitted free ONLY: tool_calling → context → speed benchmark
PHASE 4  Assign      quota + family catalog → models per combo (≥1 per combo)
PHASE 5  Publish     upsert combos in OmniRoute (atomic, ordered fallbacks)
PHASE 6  opencode.json  provider + agents + plugin block (backup, merge-preserve)
PHASE 7  Plugin      verify/install/update @omniroute/opencode-plugin-v2,
                      ensure visibleModels == the combos; auto-fix config if wrong
PHASE 8  Report      summary of everything (validated, combos, plugin state)
```

Plugin phase runs **last**, after combos and the file are written
(see §7 failure policy: interactive ask, non-TTY warn-and-continue).

---

## 3. Discovery & storage

### Endpoints
| Endpoint | Purpose |
|----------|---------|
| `GET /api/providers` | connections (accounts, `importFreeModelsOnly`, health/testStatus) |
| `GET /api/v1/models` | model list (4,900+ today, context, pricing, capabilities) |
| `GET /api/free-tier/summary` | documented free catalog (485 entries, `freeType`) |
| `GET /api/pricing/models` | provider aliases ↔ canonical slugs (match normalization) |

### Stored rows (models)
- `supports_tools`, `context_window`, `pricing_input/output`, `capabilities` (all synced models)
- `is_free`, `free_source` (`documented`|`connection`|`inferred`), `free_type`,
  `pool_key`, `free_catalog_updated_at` (migration 003/004)
- paid models stored with pricing, **never probed**
- sync filter **kept**: tool_calling + min context only; paid models without tools are
  not stored; combos form from admitted free ones.

### Free classification (kept as designed in v3.8.53 work)
- `documented`: match in free-tier catalog, `freeType ∈ {recurring-daily,
  recurring-monthly, recurring-uncapped, recurring-credit, keyless}` → admitted
  (probes + combos).
- `connection`: connection with `importFreeModelsOnly=true` → admitted.
- `inferred`: pricing `"0"` (either upstream shape) or `:free`/`-free` id suffix →
  cataloged, labeled `(free, inferred)`, **never admitted** (unless `inferred_in_combos`).
- `one-time-initial` / `discontinued` → cataloged, not admitted.
- Catalog outage at sync → previous classification preserved (fail closed).
- Catalog stale (`catalog_stale_days`, default 14) → `WARN`.
- **Paid models: cataloged, never probed.**

---

## 4. Combo assembly

### The six combos (unchanged)

| Combo | Focus | Min context | Min speed | Required capabilities |
|-------|-------|-------------|-----------|-----------------------|
| `orchestrate` | reasoning, planning, architecture, delegation | **1M** | Normal | reasoning, tool_calling, large_context |
| `code` | speed, modern code | **200k** | **Ultra** | tool_calling |
| `review` | bugs, fix planning, architecture | **1M** | Fast | reasoning, tool_calling |
| `contracts` | API contracts (OpenAPI, Orval) | **200k** | Fast | tool_calling, structured_output |
| `database` | SQL/NoSQL, migrations | **200k** | Normal | tool_calling |
| `docs` | docs, READMEs, PRs | **1M** | Slow | tool_calling |

### Model catalog — `combos.yaml` (repo, versioned, embedded)

Located at `configs/combos.yaml`, embedded into the binary, overridable via
config (`combos.catalog_file`). Structure per combo:

```yaml
combos:
  orchestrate:
    prefer:                 # ordered family globs on the model id
      - "nvidia/nemotron-*"
      - "*/deepseek-*"
      - "*/kimi-*"
    require: [reasoning, tool_calling, large_context]
  code:
    prefer:
      - "*/deepseek-*"
      - "*/step-*"
      - "*/qwen*-coder-*"
    require: [tool_calling]
```

- Matching: glob on the full `provider/model` id.
- The catalog is the **constraint**: only models whose family is listed for the
  combo can be assigned to it. Speed/context/tool probes remain **gates**.
- `require`: extra capabilities beyond the combo defaults (all must hold).
- The catalog content will ship with a first cut based on the current free catalog
  (families: Nemotron-Ultra/Super, Qwen3-Coder/Flash, DeepSeek, GPT-OSS, Kimi,
  GLM, Gemma, Devstral, Step, etc.) and be iterated by feedback.

### Rules & assignment

1. **Quota per provider** — `2 × (# connections with credentials)` distinct models
   per provider enter the pool, spread across combos. Reasoning-heavy families go to
   `orchestrate` first; fast/specialized go to the other combos.
2. **Combo eligibility** — model must be: free admitted (`documented`/`connection`),
   tool_calling passed, context ≥ combo min, speed ≥ combo min, family in combo's
   catalog.
3. **Ordering** — speed class (`ultra > fast > normal > slow`) → context window →
   catalog preference.
4. **Repetition** — `orchestrate` models may also appear in the other combos
   (reasoning-specialized work is expected to repeat); beyond that, avoid excess
   repetition between combos.
5. **Empty combo** — if no eligible model passes catalog + gates, the combo is
   **marked failed**; the rest of the run proceeds. Run reports a non-zero exit with
   the failing combos listed.
6. **≥1 model per combo** — otherwise the combo fails (see 5).

Upsert combos into OmniRoute with ordered fallbacks (`schema: priority`),
exactly like today's combo.go.

---

## 5. `opencode.json` management (OmniCatalog owns it)

OmniCatalog writes the Global config with merge-preserve of unknown keys, atomic
write, automatic backup (`*.bak-<ts>`):

```jsonc
{
  "provider": {
    "omniroute": { "name": "OmniRoute (Free Router)", "npm": "@ai-sdk/openai-compatible",
                   "options": { "baseURL": "http://localhost:20128", "modelCacheTtl": 300000, "refreshOnList": true },
                   "models": { /* the 6 combos, zero-cost markers */ } } },
  "agents": { /* the 6 combos, mode primary/subagent, system prompts — unchanged */ },
  "plugins": [ { "package": "@omniroute/opencode-plugin-v2",
                 "options": { "providerId": "omniroute", "baseURL": "http://localhost:20128",
                              "visibleModels": ["orchestrate","code","review","contracts","database","docs"],
                              "managementReadToken": "{file:~/.config/opencode/secrets/omniroute.key}" } } ]
}
```

`visibleModels` is the mechanism OpenCode uses to list **only** the combos in its
picker / model router. OmniRoute's own plugin consumes the same list.

## 6. Plugin phase (PHASE 7 of the run)

Verifies and, if needed, installs/configures `@omniroute/opencode-plugin-v2`:

1. **Presence** — `opencode plugin list` (expect `omniroute-v2` `0.1.0` or newer).
2. **Freshness** — `opencode plugin check` (expect `(current)`; else update).
3. **If missing** — `opencode plugin add @omniroute/opencode-plugin-v2`.
4. **If outdated** — `opencode plugin update @omniroute/opencode-plugin-v2`.
5. **Configuration check** (parses `opencode.json`, backed by `opencode debug config`):
   - plugin in `plugins` array with `providerId=omniroute`,
     `baseURL=http://localhost:20128`, `visibleModels` == exactly the 6 combos,
   - `managementReadToken` present (or `apiKey` fallback emits a warning),
   - `provider.omniroute.models` == the 6 combos, npm == `@ai-sdk/openai-compatible`.
6. **Auto-fix** — if anything is off, rewrite `opencode.json` atomically with backup,
   then re-verify. Fixing covers: visibleModels, provider options, provider models.

**Failure policy** — the step runs **last**; combos and the file are already written.
On any failure:
- **TTY**: ask — retry / exit non-zero (combos stay written) / continue with warn-only (run exits 0).
- **Non-TTY** (daemon/systemd): **warn + continue** (exit 0), so the pipeline never
  silently loops on a broken plugin step.

Also available standalone as `omnicatalog doctor` (same checks, **no writes**) for
diagnostics: plugin presence/version, visibleModels, provider block, management token,
combos in OmniRoute + DB.

## 7. CLI

```bash
omnicatalog run                    # full automated run (phases 1-8)
omnicatalog daemon                 # same, resident (systemd)
omnicatalog doctor                 # plugin + config + combos health check, no writes
omnicatalog status                 # last run + DB summary
omnicatalog models [--free] [--validated] [--provider groq]
omnicatalog combos [--show-current] [--diff]
omnicatalog config validate|init
omnicatalog schedule set|show
omnicatalog version
```

## 8. Configuration additions

```yaml
combos:
  catalog_file: ""        # path to a combos.yaml to override the embedded one
plugin:
  on_failure: "ask"       # ask (TTY) | warn_and_continue | fail
```

Everything else stays as is (`free_validation` incl. `inferred_in_combos`, `database`,
`logging` etc.). `free_validation.test_method` and other obsolete keys were removed in
the v3.8.53 work.

## 9. Acceptance criteria

- [ ] `omnicatalog run` completes phases 1-8 end-to-end against a live OmniRoute.
- [ ] Discovery stores free + paid models (paid never probed; tool+context filter kept).
- [ ] Free admitted (documented/connection) validated: tool/context/speed.
- [ ] **paid models are never probed** (verified: zero requests to paid ids in logs).
- [ ] Each of the 6 combos gets ≥1 model, or is explicitly failed; run reports which.
- [ ] Combos are upserted into OmniRoute with ordered fallbacks.
- [ ] `opencode.json`: provider.omniroute == 6 combos + agents + plugin block with
      visibleModels == 6 combos; atomic write + backup; unknown keys preserved.
- [ ] Plugin: installed at latest, `opencode plugin list` reports it; config
      rewritten or verified correct; non-TTY failure => warn-and-continue; TTY asks.
- [ ] `omnicatalog doctor` reports plugin/config health without writing.
- [ ] Re-runnable; systemd daemon behaves like `run`.
- [ ] Merge-preserving `opencode.json` rewrite; backup present after every run.

## 10. Risks & mitigations

| Risk | Mitigation |
|------|------------|
| Catalog (free) stale/absent | WARN on age > `catalog_stale_days`; outage → preserve previous classification (fail closed) |
| Combo with no eligible model | Combo explicitly failed; run reports non-zero; others proceed |
| Plugin missing/broken (non-TTY) | warn-and-continue; TTY asks retry/fail/continue |
| `opencode` CLI missing on host | plugin phase warns + continues (non-TTY) or asks (TTY); combos still written |
| Wrong opencode.json gets written | atomic write + automatic `*.bak-<ts>`; `omnicatalog doctor` detects it |
| Free quota mis-assignment (2/account) | Pool is per provider; reasoning families prefer `orchestrate`; orchestrate↔others repetition allowed, excess avoided elsewhere |
| opencode.json rewrite races user edits | merge-preserve + atomic write + backup; user is expected to **not** hand-edit the managed keys |

## 11. Implementation order

1. `combos.yaml` embedded + loader (`internal/combo/catalog`); glob matching; require/prefer semantics; quota distribution; ≥1 model or fail.
2. Rework `internal/combo` assignment: catalog restriction + gates + ordering (speed→context→catalog); repetition rules; per-combo failure reporting.
3. `opencode.json`: plugin phase (verify/install/update via `opencode` CLI), visibleModels management, auto-fix + re-verify; `doctor` command.
4. Rewrite README (purpose, phases, doctor, config); PLAN.md = this document.
5. Tests + live verification on the user's machine.

## 12. Out of scope (for now)

- Paid-model validation (they remain read-only catalog entries).
- `omnicatalog run --dry-run` (removed; the `run` is intentionally destructive).
- Web UI / dashboard (CLI + systemd only).
- Multi-host sync (single local OmniRoute only).
