package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config represents all omnicatalog configuration
type Config struct {
	OmniRoute    OmniRouteConfig    `mapstructure:"omniroute"`
	OpenCode     OpenCodeConfig     `mapstructure:"opencode"`
	Validation   ValidationConfig   `mapstructure:"validation"`
	SpeedTest    SpeedTestConfig    `mapstructure:"speed_test"`
	ContextVal   ContextValConfig   `mapstructure:"context_validation"`
	FreeVal      FreeValConfig      `mapstructure:"free_validation"`
	Scheduler    SchedulerConfig    `mapstructure:"scheduler"`
	Database     DatabaseConfig     `mapstructure:"database"`
	Logging      LoggingConfig      `mapstructure:"logging"`
}

type OmniRouteConfig struct {
	BaseURL string        `mapstructure:"base_url"`
	APIKey  string        `mapstructure:"api_key"`
	Timeout time.Duration `mapstructure:"timeout"`
}

type OpenCodeConfig struct {
	ConfigPath         string `mapstructure:"config_path"`
	BackupBeforeUpdate bool   `mapstructure:"backup_before_update"`
}

type ValidationConfig struct {
	APIToolCallTimeout   time.Duration `mapstructure:"api_tool_call_timeout"`
	OpenCodeTestTimeout  time.Duration `mapstructure:"opencode_test_timeout"`
	MaxConcurrentTests   int           `mapstructure:"max_concurrent_tests"`
	RevalidateAfterHours int           `mapstructure:"revalidate_after_hours"`
}

type SpeedTestConfig struct {
	WarmupRequests     int     `mapstructure:"warmup_requests"`
	TTFTSamples        int     `mapstructure:"ttft_samples"`
	ThroughputTokens   int     `mapstructure:"throughput_tokens"`
	MinTTFTMs          int     `mapstructure:"min_ttft_ms"`
	MinTPS             float64 `mapstructure:"min_tps"`
	ToolCallSamples    int     `mapstructure:"tool_call_samples"`
}

type ContextValConfig struct {
	Enabled                bool `mapstructure:"enabled"`
	TestTokens             int  `mapstructure:"test_tokens"`
	MinContextOrchestrate  int  `mapstructure:"min_context_orchestrate"`
	MinContextCode         int  `mapstructure:"min_context_code"`
	MinContextReview       int  `mapstructure:"min_context_review"`
	MinContextContracts    int  `mapstructure:"min_context_contracts"`
	MinContextDatabase     int  `mapstructure:"min_context_database"`
	MinContextDocs         int  `mapstructure:"min_context_docs"`
}

type FreeValConfig struct {
	Enabled                bool   `mapstructure:"enabled"`
	WindowPerProviderSec   int    `mapstructure:"window_per_provider_sec"`
	MaxConcurrentProviders int    `mapstructure:"max_concurrent_providers"`
	TestMethod             string `mapstructure:"test_method"` // "billing_check" | "quota_endpoint" | "usage_headers"
	CacheTTLHours          int    `mapstructure:"cache_ttl_hours"`
}

type SchedulerConfig struct {
	Enabled      bool   `mapstructure:"enabled"`
	Schedule     string `mapstructure:"schedule"`    // "daily", "weekly", "custom"
	CustomCron   string `mapstructure:"custom_cron"` // cron expression
	RunOnStartup bool   `mapstructure:"run_on_startup"`
}

type DatabaseConfig struct {
	Path         string `mapstructure:"path"`
	MigrationDir string `mapstructure:"migration_dir"`
}

type LoggingConfig struct {
	Level    string `mapstructure:"level"`      // debug, info, warn, error
	Format   string `mapstructure:"format"`     // json, text
	Output   string `mapstructure:"output"`     // stdout, file, both
	FilePath string `mapstructure:"file_path"`
}

// Load loads configuration from YAML file, env vars, and file secrets
func Load() (*Config, error) {
	v := viper.New()

	// Defaults
	setDefaults(v)

	// Config file via env var (highest priority)
	envConfigSet := false
	if configFile := os.Getenv("OMNICATALOG_CONFIG"); configFile != "" {
		expanded := expandPath(configFile)
		if _, err := os.Stat(expanded); err == nil {
			v.SetConfigFile(expanded)
			if err := v.ReadInConfig(); err != nil {
				return nil, fmt.Errorf("error reading config %s: %w", expanded, err)
			}
			envConfigSet = true
		}
	}

	// Config file paths (priority order)
	if !envConfigSet {
		configPaths := []string{
		"/etc/omnicatalog/config.yaml",
		"$HOME/.config/omnicatalog/config.yaml",
		"./config.yaml",
		"./configs/config.yaml",
	}

	for _, path := range configPaths {
		expanded := expandPath(path)
		if _, err := os.Stat(expanded); err == nil {
			v.SetConfigFile(expanded)
			if err := v.ReadInConfig(); err != nil {
				return nil, fmt.Errorf("error reading config %s: %w", expanded, err)
			}
			break
		}
	}
	}

	// Environment variables (prefix OMNICATALOG_)
	v.SetEnvPrefix("OMNICATALOG")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// File secrets (ex: {file:/path/to/secret})
	v.SetConfigType("yaml")

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("error unmarshaling config: %w", err)
	}

	// Expand paths in config
	cfg.OpenCode.ConfigPath = expandPath(cfg.OpenCode.ConfigPath)
	cfg.Database.Path = expandPath(cfg.Database.Path)
	cfg.Database.MigrationDir = expandPath(cfg.Database.MigrationDir)
	cfg.Logging.FilePath = expandPath(cfg.Logging.FilePath)

	// Resolve API key from file secret if needed
	cfg.OmniRoute.APIKey = resolveSecret(cfg.OmniRoute.APIKey)

	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	// OmniRoute
	v.SetDefault("omniroute.base_url", "http://localhost:20128")
	v.SetDefault("omniroute.timeout", "30s")

	// OpenCode
	v.SetDefault("opencode.config_path", "~/.config/opencode/opencode.json")
	v.SetDefault("opencode.backup_before_update", true)

	// Validation
	v.SetDefault("validation.api_tool_call_timeout", "30s")
	v.SetDefault("validation.opencode_test_timeout", "60s")
	v.SetDefault("validation.max_concurrent_tests", 3)
	v.SetDefault("validation.revalidate_after_hours", 24)

	// Speed Test
	v.SetDefault("speed_test.warmup_requests", 1)
	v.SetDefault("speed_test.ttft_samples", 3)
	v.SetDefault("speed_test.throughput_tokens", 500)
	v.SetDefault("speed_test.min_ttft_ms", 3000)
	v.SetDefault("speed_test.min_tps", 20.0)
	v.SetDefault("speed_test.tool_call_samples", 2)

	// Context Validation
	v.SetDefault("context_validation.enabled", true)
	v.SetDefault("context_validation.test_tokens", 100000)
	v.SetDefault("context_validation.min_context_orchestrate", 1000000)
	v.SetDefault("context_validation.min_context_code", 200000)
	v.SetDefault("context_validation.min_context_review", 1000000)
	v.SetDefault("context_validation.min_context_contracts", 200000)
	v.SetDefault("context_validation.min_context_database", 200000)
	v.SetDefault("context_validation.min_context_docs", 1000000)

	// Free Validation
	v.SetDefault("free_validation.enabled", true)
	v.SetDefault("free_validation.window_per_provider_sec", 30)
	v.SetDefault("free_validation.max_concurrent_providers", 5)
	v.SetDefault("free_validation.test_method", "billing_check")
	v.SetDefault("free_validation.cache_ttl_hours", 24)

	// Scheduler
	v.SetDefault("scheduler.enabled", true)
	v.SetDefault("scheduler.schedule", "daily")
	v.SetDefault("scheduler.custom_cron", "")
	v.SetDefault("scheduler.run_on_startup", true)

	// Database
	v.SetDefault("database.path", "~/.local/share/omnicatalog/catalog.db")
	v.SetDefault("database.migration_dir", "./migrations")

	// Logging
	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.format", "text")
	v.SetDefault("logging.output", "stdout")
	v.SetDefault("logging.file_path", "/var/log/omnicatalog.log")
}

func expandPath(path string) string {
	if path == "" {
		return path
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	if strings.HasPrefix(path, "$") {
		return os.ExpandEnv(path)
	}
	return path
}

func resolveSecret(value string) string {
	if strings.HasPrefix(value, "{env:") && strings.HasSuffix(value, "}") {
		varName := value[5 : len(value)-1]
		if v := os.Getenv(varName); v != "" {
			return v
		}
	}
	if strings.HasPrefix(value, "{file:") && strings.HasSuffix(value, "}") {
		path := expandPath(value[6 : len(value)-1])
		if data, err := os.ReadFile(path); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return value
}

// MinContextForCombo returns minimum context for a specific combo
func (c *ContextValConfig) MinContextForCombo(combo string) int {
	switch combo {
	case "orchestrate":
		return c.MinContextOrchestrate
	case "code":
		return c.MinContextCode
	case "review":
		return c.MinContextReview
	case "contracts":
		return c.MinContextContracts
	case "database":
		return c.MinContextDatabase
	case "docs":
		return c.MinContextDocs
	default:
		return 200000
	}
}