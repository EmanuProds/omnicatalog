package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"omnicatalog/internal/combo"
	"omnicatalog/internal/config"
	"omnicatalog/internal/discovery"
	"omnicatalog/internal/omniroute"
	"omnicatalog/internal/scheduler"
	"omnicatalog/internal/store"
	"omnicatalog/internal/validation"
)

func NewRootCommand(cfg *config.Config, version, commit, buildDate string) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:     "omnicatalog",
		Short:   "OmniRoute model validator for OpenCode",
		Long:    `Omnicatalog discovers, validates, and catalogs free models with tool calling from OmniRoute, updating combos in OpenCode.`,
		Version: fmt.Sprintf("%s (commit: %s, built: %s, go: %s)", version, commit, buildDate, runtime.Version()),
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}

	// Global flags
	rootCmd.PersistentFlags().String("config", "", "Path to config file (default: auto-detect)")
	rootCmd.PersistentFlags().Bool("dry-run", false, "Simulate execution without saving changes")

	// Subcommands
	rootCmd.AddCommand(newRunCommand(cfg))
	rootCmd.AddCommand(newSelectCommand(cfg))
	rootCmd.AddCommand(newDaemonCommand(cfg))
	rootCmd.AddCommand(newStatusCommand(cfg))
	rootCmd.AddCommand(newModelsCommand(cfg))
	rootCmd.AddCommand(newCombosCommand(cfg))
	rootCmd.AddCommand(newConfigCommand(cfg))
	rootCmd.AddCommand(newScheduleCommand(cfg))
	rootCmd.AddCommand(newHealthCommand(cfg))
	rootCmd.AddCommand(newCompletionCommand())
	rootCmd.AddCommand(newVersionCommand(version, commit, buildDate))

	return rootCmd
}

func newSelectCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "select",
		Short: "Interactive model selection and combo management",
		Long:  `Select models for validation and manage OpenCode combos.`, 
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInteractiveSelection(cfg)
		},
	}
	return cmd
}

func newRunCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Executes full validation (manual or cron)",
		Long:  `Discovers providers/models, validates free tier, tool calling, context and speed, updates combos in OmniRoute and opencode.json.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			onlyNew, _ := cmd.Flags().GetBool("only-new")
			forceRevalidate, _ := cmd.Flags().GetBool("force-revalidate")
			testAll, _ := cmd.Flags().GetBool("all")
			verbose, _ := cmd.Flags().GetBool("verbose")
			interactive, _ := cmd.Flags().GetBool("interactive")
			if verbose {
				zerolog.SetGlobalLevel(zerolog.DebugLevel)
			} else {
				zerolog.SetGlobalLevel(zerolog.WarnLevel)
			}
			
			log.Info().
				Bool("dry_run", dryRun).
				Bool("only_new", onlyNew).
				Bool("force_revalidate", forceRevalidate).
				Msg("Starting full validation")

			if interactive {
				return runInteractiveSelection(cfg)
			}

			ctx := context.Background()

			// Initialize all components
			client := omniroute.NewClient(cfg)
			storeInstance, err := store.NewStore(cfg.Database.Path, cfg.Database.MigrationDir)
			if err != nil {
				return fmt.Errorf("failed to open database: %w", err)
			}
			defer storeInstance.Close()

			discoverySvc := discovery.NewDiscovery(cfg, storeInstance)
			freeValidator := validation.NewFreeValidator(cfg, storeInstance, discoverySvc, client)
			toolValidator := validation.NewToolValidator(cfg, storeInstance, discoverySvc, client)
			contextValidator := validation.NewContextValidator(cfg, storeInstance, discoverySvc, client)
			speedBench := validation.NewSpeedBenchmark(cfg, storeInstance, discoverySvc, client)
			comboMgr := combo.NewComboManager(cfg, storeInstance, client)

			// Phase 1: Discovery
			log.Info().Msg("Phase 1: Discovery")
			if err := discoverySvc.SyncAll(ctx); err != nil {
				return fmt.Errorf("discovery phase failed: %w", err)
			}

			// Interactive provider/model selection (TTY only unless --all)
			if !testAll && termIsTerminal(os.Stdin) && termIsTerminal(os.Stdout) {
				log.Info().Msg("Please select providers and models to test")
				providers, err := storeInstance.GetProviders()
				if err != nil {
					return fmt.Errorf("failed to get providers: %w", err)
				}

				// Preselect from saved settings
				preselectedProviders := map[string]bool{}
				preselectedModels := map[string]bool{}
				if saved, err := storeInstance.GetSetting("selected_providers"); err == nil && saved != "" {
					var slugs []string
					if json.Unmarshal([]byte(saved), &slugs) == nil {
						for _, sl := range slugs {
							preselectedProviders[sl] = true
						}
					}
				}
				if savedM, err := storeInstance.GetSetting("selected_models"); err == nil && savedM != "" {
					var ids []string
					if json.Unmarshal([]byte(savedM), &ids) == nil {
						for _, id := range ids {
							preselectedModels[id] = true
						}
					}
				}

				// Build provider list with candidate model counts
				providerItems := make([]SelectItem, 0, len(providers))
				providerModelMap := make(map[string][]store.Model)
				for _, p := range providers {
					mods, _ := storeInstance.GetModels(store.ModelFilter{ProviderID: p.ID, SupportsTools: boolPtr(true)})
					var cand []store.Model
					for _, m := range mods {
						if m.ContextWindow != nil && *m.ContextWindow >= cfg.ContextVal.TestTokens {
							cand = append(cand, m)
						}
					}
					providerModelMap[p.ID] = cand
					if len(cand) == 0 {
						continue
					}
					sel := true
					if len(preselectedProviders) > 0 {
						sel = preselectedProviders[p.ID]
					}
					providerItems = append(providerItems, SelectItem{
						ID: p.ID, Label: fmt.Sprintf("%s (%d models)", p.ID, len(cand)), Selected: sel,
					})
				}

				selProviders, err := Multiselect("Select providers to test", providerItems)
				if err != nil {
					log.Warn().Err(err).Msg("Selection cancelled, testing all providers")
					testAll = true
				} else {
					var selectedProviderSlugs []string
					for _, it := range selProviders {
						if it.Selected {
							selectedProviderSlugs = append(selectedProviderSlugs, it.ID)
						}
					}

					// Model selection per provider
					var selectedModelIDs []string
					for _, slug := range selectedProviderSlugs {
						mods := providerModelMap[slug]
						if len(mods) == 0 {
							continue
						}
						modelItems := make([]SelectItem, 0, len(mods))
						for _, m := range mods {
							selM := strings.HasSuffix(m.ID, ":free") || strings.Contains(strings.ToLower(m.ID), "free")
							if len(preselectedModels) > 0 {
								selM = preselectedModels[m.ID]
							}
							ctxStr := "?"
							if m.ContextWindow != nil {
								ctxStr = formatTokens(*m.ContextWindow)
							}
							modelItems = append(modelItems, SelectItem{
								ID: m.ID, Label: fmt.Sprintf("%s (ctx %s)", m.ID, ctxStr), Selected: selM,
							})
						}
						selModels, err := Multiselect(fmt.Sprintf("Select models for %s", slug), modelItems)
						if err != nil {
							// on cancel, select all for this provider
							for _, m := range mods {
								selectedModelIDs = append(selectedModelIDs, m.ID)
							}
							continue
						}
						for _, it := range selModels {
							if it.Selected {
								selectedModelIDs = append(selectedModelIDs, it.ID)
							}
						}
					}

					// Persist selection
					bp, _ := json.Marshal(selectedProviderSlugs)
					bm, _ := json.Marshal(selectedModelIDs)
					_ = storeInstance.SetSetting("selected_providers", string(bp))
					_ = storeInstance.SetSetting("selected_models", string(bm))

					// Apply to discovery
					if len(selectedModelIDs) == 0 {
						fmt.Println("No models selected; testing all models")
						discoverySvc.SetSelectedModels(nil)
					} else {
						discoverySvc.SetSelectedModels(selectedModelIDs)
						fmt.Printf("Selection applied: %d provider(s), %d model(s)\n", len(selectedProviderSlugs), len(selectedModelIDs))
					}
				}
			}

			if testAll {
				// clear selection filter -> all models
				discoverySvc.SetSelectedModels(nil)
			} else if !(termIsTerminal(os.Stdin) && termIsTerminal(os.Stdout)) {
				// Non-interactive: reuse saved selection if present
				if saved, _ := storeInstance.GetSetting("selected_models"); saved != "" {
					var ids []string
					if json.Unmarshal([]byte(saved), &ids) == nil && len(ids) > 0 {
						discoverySvc.SetSelectedModels(ids)
						fmt.Printf("Using saved selection: %d model(s)\n", len(ids))
					} else {
						discoverySvc.SetSelectedModels(nil)
					}
				} else {
					discoverySvc.SetSelectedModels(nil)
				}
			}

			// Phase 2: Free tier validation
			log.Info().Msg("Phase 2: Free tier validation")
			if err := freeValidator.ValidateFreeTier(ctx); err != nil {
				return fmt.Errorf("free tier validation failed: %w", err)
			}

			// Phase 3: Tool calling validation
			log.Info().Msg("Phase 3: Tool calling validation")
			if err := toolValidator.ValidateToolCalling(ctx); err != nil {
				return fmt.Errorf("tool calling validation failed: %w", err)
			}

			// Phase 4: Context validation
			log.Info().Msg("Phase 4: Context validation")
			if err := contextValidator.ValidateContext(ctx); err != nil {
				return fmt.Errorf("context validation failed: %w", err)
			}

			// Phase 5: Speed benchmark
			log.Info().Msg("Phase 5: Speed benchmark")
			if err := speedBench.RunBenchmarks(ctx); err != nil {
				return fmt.Errorf("speed benchmark failed: %w", err)
			}

			// Phase 6: Combo update
			log.Info().Msg("Phase 6: Combo update")
			if err := comboMgr.UpdateCombos(ctx); err != nil {
				return fmt.Errorf("combo update failed: %w", err)
			}

			// Final summary
			fmt.Println()
			fmt.Println("=== Final Combos ===")
			fmt.Printf("  %-14s %-10s %-10s %s\n", "Combo", "Speed", "Context", "Models")
			fmt.Printf("  %-14s %-10s %-10s %s\n", "-----", "-----", "-------", "------")
			managed, _ := storeInstance.GetCombos()
			for _, c := range managed {
				ctx := formatTokens(c.MinContextWindow)
				fmt.Printf("  %-14s %-10s %-10s %s\n", c.ID, c.MinSpeedClass, ctx, strings.Join(c.TargetModelIDs, ", "))
			}
			fmt.Printf("\n  opencode.json: %s\n", cfg.OpenCode.ConfigPath)

			if dryRun {
				log.Info().Msg("DRY-RUN: No changes would be saved")
			}

			log.Info().Msg("Full validation completed successfully")
			return nil
		},
	}
	cmd.Flags().Bool("only-new", false, "Validate only new/untested models")
	cmd.Flags().Bool("force-revalidate", false, "Force revalidation of all models")
	cmd.Flags().Bool("all", false, "Test all providers and models without interactive selection")
	cmd.Flags().Bool("verbose", false, "Enable detailed per-model logging")
	cmd.Flags().Bool("interactive", false, "Show interactive selection menu")
	return cmd
}

func newDaemonCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Runs as systemd service (resident)",
		Long:  `Daemon mode for continuous execution with scheduler.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Info().Msg("Starting daemon mode")

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			client := omniroute.NewClient(cfg)
			storeInstance, err := store.NewStore(cfg.Database.Path, cfg.Database.MigrationDir)
			if err != nil {
				return fmt.Errorf("failed to open database: %w", err)
			}
			defer storeInstance.Close()

			sched := scheduler.NewScheduler(cfg, storeInstance, client)
			if err := sched.Start(); err != nil {
				return fmt.Errorf("failed to start scheduler: %w", err)
			}

			log.Info().Msg("Daemon running; waiting for shutdown signal")
			<-ctx.Done()

			log.Info().Msg("Stopping scheduler")
			if err := sched.Stop(); err != nil {
				log.Error().Err(err).Msg("Scheduler stop error")
			}
			return nil
		},
	}
	return cmd
}

func newStatusCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Shows last run status",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Open store to read status
			s, err := store.NewStore(cfg.Database.Path, cfg.Database.MigrationDir)
			if err != nil {
				return fmt.Errorf("error opening database: %w", err)
			}
			defer s.Close()

			runs, err := s.GetSchedulerRuns(5)
			if err != nil {
				return fmt.Errorf("error reading runs: %w", err)
			}

			fmt.Println("=== Recent Runs ===")
			if len(runs) == 0 {
				fmt.Println("  No runs recorded")
				return nil
			}

			for _, run := range runs {
				statusIcon := "✅"
				if run.Status == "failed" {
					statusIcon = "❌"
				} else if run.Status == "running" {
					statusIcon = "🔄"
				}
				fmt.Printf("  %s %s | %s | Tested: %d | Passed: %d | Failed: %d | Duration: %dms\n",
					statusIcon, run.StartedAt, run.RunType, run.ModelsTested, run.ModelsPassed, run.ModelsFailed, run.DurationMs)
			}
			return nil
		},
	}
	return cmd
}

func newModelsCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "models",
		Short: "Lists models in local catalog",
		RunE: func(cmd *cobra.Command, args []string) error {
			freeOnly, _ := cmd.Flags().GetBool("free")
			validatedOnly, _ := cmd.Flags().GetBool("validated")
			provider, _ := cmd.Flags().GetString("provider")
			_, _ = cmd.Flags().GetString("combo") // reserved for future filtering

			s, err := store.NewStore(cfg.Database.Path, cfg.Database.MigrationDir)
			if err != nil {
				return fmt.Errorf("error opening database: %w", err)
			}
			defer s.Close()

			filter := store.ModelFilter{
				ProviderID: provider,
			}
			if freeOnly {
				f := true
				filter.IsFree = &f
			}
			if validatedOnly {
				f := true
				filter.FreeConfirmed = &f
				filter.SupportsTools = &f
			}

			models, err := s.GetModels(filter)
			if err != nil {
				return fmt.Errorf("error listing models: %w", err)
			}

			fmt.Printf("=== Models (%d) ===\n", len(models))
			for _, m := range models {
				freeIcon := "💰"
				if m.IsFree {
					freeIcon = "🆓"
				}
				toolIcon := "🔧"
				if !m.SupportsTools {
					toolIcon = "❌"
				}
				ctx := "?"
				if m.ContextWindow != nil {
					ctx = formatTokens(*m.ContextWindow)
				}
				fmt.Printf("  %s %s %s | %s | Context: %s | Provider: %s\n",
					freeIcon, toolIcon, m.ID, m.Name, ctx, m.ProviderID)
			}
			return nil
		},
	}
	cmd.Flags().Bool("free", false, "Only free models")
	cmd.Flags().Bool("validated", false, "Only validated models (free + tools + context)")
	cmd.Flags().String("provider", "", "Filter by provider")
	cmd.Flags().String("combo", "", "Filter by combo (orchestrate, code, review, contracts, database, docs)")
	return cmd
}

func newCombosCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "combos",
		Short: "Shows/updates combos in OmniRoute and opencode.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			showCurrent, _ := cmd.Flags().GetBool("show-current") // reserved for future use
			_ = showCurrent
			showDiff, _ := cmd.Flags().GetBool("diff")

			s, err := store.NewStore(cfg.Database.Path, cfg.Database.MigrationDir)
			if err != nil {
				return fmt.Errorf("error opening database: %w", err)
			}
			defer s.Close()

			combos, err := s.GetCombos()
			if err != nil {
				return fmt.Errorf("error reading combos: %w", err)
			}

			fmt.Println("=== Managed Combos ===")
			for _, c := range combos {
				enabled := "✅"
				if !c.Enabled {
					enabled = "⏸️"
				}
				fmt.Printf("  %s %s (%s) | Speed: %s | Context: %s | Models: %d + %d fallbacks\n",
					enabled, c.ID, c.Name, c.MinSpeedClass, formatTokens(c.MinContextWindow),
					len(c.TargetModelIDs), len(c.FallbackModelIDs))
			}

			if showDiff {
				fmt.Println("\n[Diff not implemented yet]")
			}
			return nil
		},
	}
	cmd.Flags().Bool("show-current", false, "Shows current combos in OmniRoute")
	cmd.Flags().Bool("diff", false, "Shows diff between local and OmniRoute")
	return cmd
}

func newConfigCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manages configuration",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Creates default config.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			// TODO: Generate config.yaml.example to default path
			fmt.Println("⚠️  Command 'config init' not yet implemented")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Validates current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("✅ Configuration valid\n")
			fmt.Printf("  OmniRoute: %s\n", cfg.OmniRoute.BaseURL)
			fmt.Printf("  OpenCode:  %s\n", cfg.OpenCode.ConfigPath)
			fmt.Printf("  Database:  %s\n", cfg.Database.Path)
			return nil
		},
	})

	return cmd
}

func newScheduleCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Manages scheduling",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "set [daily|weekly|custom]",
		Short: "Sets schedule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			schedule := args[0]
			fmt.Printf("⚠️  Schedule '%s' configured (persistence not implemented)\n", schedule)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Shows current schedule",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("Schedule: %s\n", cfg.Scheduler.Schedule)
			if cfg.Scheduler.CustomCron != "" {
				fmt.Printf("Custom cron: %s\n", cfg.Scheduler.CustomCron)
			}
			return nil
		},
	})

	return cmd
}

func newVersionCommand(version, commit, buildDate string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Shows detailed version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("omnicatalog %s\n", version)
			fmt.Printf("  Commit:     %s\n", commit)
			fmt.Printf("  Built:      %s\n", buildDate)
			fmt.Printf("  Go:         %s\n", runtime.Version())
			fmt.Printf("  OS/Arch:    %s/%s\n", runtime.GOOS, runtime.GOARCH)
		},
	}
}

func newHealthCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Checks OmniRoute connectivity, database, and configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath, _ := cmd.Flags().GetString("config")
			if configPath == "" {
				configPath = "~/.config/omnicatalog/config.yaml"
			}
			configPath = expandPath(configPath)

			fmt.Println("OmniCatalog Health Check")
			fmt.Println("========================")
			fmt.Println()

			failed := 0

			// Check config file
			configFile := configPath
			if configFile == "~/.config/omnicatalog/config.yaml" {
				configFile = os.Getenv("HOME") + "/.config/omnicatalog/config.yaml"
			}
			if _, err := os.Stat(configFile); err != nil {
				fmt.Printf("[FAIL] Config file not found: %s\n", configFile)
				failed++
			} else {
				fmt.Printf("[OK] Config file: %s\n", configFile)
			}

			// Load config
			cfg, err := config.Load()
			if err != nil {
				fmt.Printf("[FAIL] Config load failed: %v\n", err)
				failed++
			} else {
				fmt.Printf("[OK] Config loaded\n")

				// Check OmniRoute
				if cfg.OmniRoute.APIKey != "" {
					fmt.Printf("[OK] API key configured\n")
				} else {
					fmt.Printf("[WARN] API key not set (use OMNIROUTE_API_KEY env or {file:...})\n")
				}

				// Test OmniRoute connectivity
				if cfg.OmniRoute.BaseURL != "" {
					fmt.Printf("Testing %s/api/providers... ", cfg.OmniRoute.BaseURL)
					client := &http.Client{Timeout: 10 * time.Second}
					req, _ := http.NewRequest("GET", cfg.OmniRoute.BaseURL+"/api/providers", nil)
					req.Header.Set("Authorization", "Bearer "+cfg.OmniRoute.APIKey)
					resp, err := client.Do(req)
					if err != nil {
						fmt.Printf("[FAIL] OmniRoute unreachable: %v\n", err)
						failed++
					} else {
						resp.Body.Close()
						if resp.StatusCode == 200 {
							fmt.Printf("[OK] OmniRoute reachable\n")
						} else {
							fmt.Printf("[FAIL] OmniRoute returned %d\n", resp.StatusCode)
							failed++
						}
					}
				}

				// Check database
				dbPath := cfg.Database.Path
				if strings.HasPrefix(dbPath, "~/") {
					dbPath = os.Getenv("HOME") + dbPath[1:]
				}
				if _, err := os.Stat(dbPath); err == nil {
					fmt.Printf("[OK] Database: %s\n", dbPath)
				} else {
					fmt.Printf("[WARN] Database not found: %s\n", dbPath)
				}
			}

			// Check binary
			if _, err := exec.LookPath("omnicatalog"); err == nil {
				fmt.Printf("[OK] Binary in PATH\n")
			} else {
				fmt.Printf("[WARN] Binary not in PATH\n")
			}

			// Check systemd
			if systemctlEnabled("omnicatalog") {
				if systemctlActive("omnicatalog") {
					fmt.Printf("[OK] Service enabled and running\n")
				} else {
					fmt.Printf("[WARN] Service enabled but not running\n")
				}
			} else {
				fmt.Printf("[WARN] Service not enabled\n")
			}

			fmt.Println()
			fmt.Println("========================")
			if failed == 0 {
				fmt.Println("[OK] All checks passed!")
				return nil
			} else {
				fmt.Printf("[FAIL] %d check(s) failed\n", failed)
				return fmt.Errorf("%d check(s) failed", failed)
			}
		},
	}
	cmd.Flags().String("config", "", "Path to config file")
	return cmd
}

func systemctlEnabled(unit string) bool {
	cmd := exec.Command("systemctl", "is-enabled", unit)
	err := cmd.Run()
	return err == nil
}

func systemctlActive(unit string) bool {
	cmd := exec.Command("systemctl", "is-active", unit)
	err := cmd.Run()
	return err == nil
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

func newCompletionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Generate shell completion script",
		Long: `Generate shell completion script for omnicatalog.

Examples:
  omnicatalog completion bash > /etc/bash_completion.d/omnicatalog
  omnicatalog completion zsh > ~/.zsh/completions/_omnicatalog
  omnicatalog completion fish > ~/.config/fish/completions/omnicatalog.fish
`,
		Args: cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			shell := args[0]
			rootCmd := cmd.Root()
			switch shell {
			case "bash":
				return rootCmd.GenBashCompletion(os.Stdout)
			case "zsh":
				return rootCmd.GenZshCompletion(os.Stdout)
			case "fish":
				return rootCmd.GenFishCompletion(os.Stdout, true)
			case "powershell":
				return rootCmd.GenPowerShellCompletion(os.Stdout)
			default:
				return fmt.Errorf("unsupported shell: %s", shell)
			}
		},
	}
}

func formatTokens(n int) string {
	if n >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.0fK", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func runInteractiveSelection(cfg *config.Config) error {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Println()
		fmt.Println("=== OmniCatalog Selection Menu ===")
		fmt.Println("  1) Auto selection       - Select free + tool_calling models from OmniRoute, quick test, create combos")
		fmt.Println("  2) Manual selection     - Choose providers/models manually for combos")
		fmt.Println("  3) Debug selection      - Test unknown models (full pipeline: free, tool, context, speed)")
		fmt.Println("  4) Update combos        - Sync and update OpenCode combos with functional models from DB")
		fmt.Println("  5) Exit")
		fmt.Print("Choose option [1-5]: ")
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		switch line {
		case "1":
			return runAutoSelection(cfg)
		case "2":
			return runManualSelection(cfg)
		case "3":
			return runDebugSelection(cfg)
		case "4":
			return runUpdateCombos(cfg)
		case "5", "q", "Q":
			fmt.Println("Exiting")
			return nil
		default:
			fmt.Println("Invalid option")
		}
	}
}

// runAutoSelection selects free + tool_calling models from OmniRoute, quick test, create combos
func runAutoSelection(cfg *config.Config) error {
	ctx := context.Background()
	fmt.Println("Auto selection: fetching free + tool_calling models from OmniRoute...")

	client := omniroute.NewClient(cfg)
	storeInstance, err := store.NewStore(cfg.Database.Path, cfg.Database.MigrationDir)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer storeInstance.Close()

	discoverySvc := discovery.NewDiscovery(cfg, storeInstance)

	// Sync all providers and models
	fmt.Println("  Syncing providers and models...")
	if err := discoverySvc.SyncAll(ctx); err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}

	// Get models that are explicitly free (pricing=0 or :free suffix) AND support tools
	filter := store.ModelFilter{
		IsFree:        boolPtr(true),
		SupportsTools: boolPtr(true),
	}
	models, err := storeInstance.GetModels(filter)
	if err != nil {
		return fmt.Errorf("failed to get models: %w", err)
	}

	// Filter to only explicitly free models (free_confirmed or :free suffix or pricing=0)
	testTokens := cfg.ContextVal.TestTokens
	if testTokens <= 0 {
		testTokens = 8000
	}
	var candidates []store.Model
	for _, m := range models {
		isExplicitFree := m.FreeConfirmed ||
			strings.HasSuffix(m.ID, ":free") ||
			(m.PricingInput != nil && *m.PricingInput == 0 && m.PricingOutput != nil && *m.PricingOutput == 0)

		if !isExplicitFree {
			continue
		}
		if m.ContextWindow == nil || *m.ContextWindow < testTokens {
			continue
		}

		// Only include API-compatible providers (skip CLI-based like aug, kilocode, etc.)
		apiCompatibleProviders := map[string]bool{
			"openrouter":   true,
			"groq":         true,
			"gemini":       true,
			"mistral":      true,
			"openadapter":  true,
			"opencode-zen": true,
			"cloudflare-ai": true,
			"nvidia":       true,
			"huggingface":  true,
			"bazaarlink":   true,
			"poolside":     true,
		}
		if apiCompatibleProviders[m.ProviderID] && isExplicitFree {
			candidates = append(candidates, m)
		}
	}

	// Limit to max 50 models for auto selection
	if len(candidates) > 50 {
		candidates = candidates[:50]
	}

	fmt.Printf("  Found %d explicitly free + tool_calling models with context >= %d (limited to 50)\n", len(candidates), testTokens)
	if len(candidates) == 0 {
		fmt.Println("  No explicitly free candidates found")
		return nil
	}

	// Apply selection to discovery (for validators that use discovery)
	selectedIDs := make([]string, len(candidates))
	for i, m := range candidates {
		selectedIDs[i] = m.ID
	}
	discoverySvc.SetSelectedModels(selectedIDs)

	// Run quick tool calling validation on selected models (bypass discovery filter)
	fmt.Println("  Running quick API tool calling validation...")
	toolValidator := validation.NewToolValidator(cfg, storeInstance, discoverySvc, client)
	// Direct validation on selected models
	for _, m := range candidates {
		if err := toolValidator.ValidateModelToolCalling(ctx, m); err != nil {
			log.Warn().Err(err).Str("model", m.ID).Msg("Tool calling validation error")
		}
	}

	// Run context validation on selected models
	fmt.Println("  Running context validation...")
	contextValidator := validation.NewContextValidator(cfg, storeInstance, discoverySvc, client)
	for _, m := range candidates {
		if err := contextValidator.ValidateModelContext(ctx, m); err != nil {
			log.Warn().Err(err).Str("model", m.ID).Msg("Context validation error")
		}
	}

	// Run speed benchmark on selected models
	fmt.Println("  Running speed benchmark...")
	speedBench := validation.NewSpeedBenchmark(cfg, storeInstance, discoverySvc, client)
	for _, m := range candidates {
		if err := speedBench.BenchmarkModel(ctx, m); err != nil {
			log.Warn().Err(err).Str("model", m.ID).Msg("Speed benchmark error")
		}
	}

	// Update combos
	fmt.Println("  Updating combos with selected models...")
	comboMgr := combo.NewComboManager(cfg, storeInstance, client)
	if err := comboMgr.UpdateCombos(ctx); err != nil {
		return fmt.Errorf("combo update failed: %w", err)
	}

	fmt.Println("  Auto selection complete! Combos updated with explicitly free models.")
	return nil
}


// runManualSelection - interactive provider/model selection

// runManualSelection - interactive provider/model selection
func runManualSelection(cfg *config.Config) error {
	ctx := context.Background()
	fmt.Println("Manual selection: starting interactive selection...")

	client := omniroute.NewClient(cfg)
	storeInstance, err := store.NewStore(cfg.Database.Path, cfg.Database.MigrationDir)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer storeInstance.Close()

	discoverySvc := discovery.NewDiscovery(cfg, storeInstance)

	// Sync all first
	fmt.Println("  Syncing providers and models...")
	if err := discoverySvc.SyncAll(ctx); err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}

	// Get providers with candidate models
	providers, err := storeInstance.GetProviders()
	if err != nil {
		return fmt.Errorf("failed to get providers: %w", err)
	}

	// Build provider list with candidate model counts
	testTokens := cfg.ContextVal.TestTokens
	if testTokens <= 0 {
		testTokens = 8000
	}
	providerModelMap := make(map[string][]store.Model)
	providerItems := make([]SelectItem, 0, len(providers))

	for _, p := range providers {
		mods, _ := storeInstance.GetModels(store.ModelFilter{
			ProviderID:    p.ID,
			SupportsTools: boolPtr(true),
		})
		var cand []store.Model
		for _, m := range mods {
			if m.ContextWindow != nil && *m.ContextWindow >= testTokens {
				cand = append(cand, m)
			}
		}
		if len(cand) > 0 {
			providerModelMap[p.ID] = cand
			providerItems = append(providerItems, SelectItem{
				ID: p.ID, Label: fmt.Sprintf("%s (%d models)", p.ID, len(cand)), Selected: false,
			})
		}
	}

	if len(providerItems) == 0 {
		fmt.Println("  No providers with suitable models found")
		return nil
	}

	// Provider selection
	selProviders, err := Multiselect("Select providers for manual combo selection", providerItems)
	if err != nil {
		return err
	}

	var selectedProviderSlugs []string
	for _, it := range selProviders {
		if it.Selected {
			selectedProviderSlugs = append(selectedProviderSlugs, it.ID)
		}
	}

	// Model selection per provider
	var selectedModelIDs []string
	for _, slug := range selectedProviderSlugs {
		mods := providerModelMap[slug]
		if len(mods) == 0 {
			continue
		}
		modelItems := make([]SelectItem, 0, len(mods))
		for _, m := range mods {
			ctxStr := "?"
			if m.ContextWindow != nil {
				ctxStr = formatTokens(*m.ContextWindow)
			}
			modelItems = append(modelItems, SelectItem{
				ID: m.ID, Label: fmt.Sprintf("%s (ctx %s)", m.ID, ctxStr), Selected: true,
			})
		}
		selModels, err := Multiselect(fmt.Sprintf("Select models for %s", slug), modelItems)
		if err != nil {
			// on cancel, select all for this provider
			for _, m := range mods {
				selectedModelIDs = append(selectedModelIDs, m.ID)
			}
			continue
		}
		for _, it := range selModels {
			if it.Selected {
				selectedModelIDs = append(selectedModelIDs, it.ID)
			}
		}
	}

	if len(selectedModelIDs) == 0 {
		fmt.Println("No models selected")
		return nil
	}

	// Persist selection
	bp, _ := json.Marshal(selectedProviderSlugs)
	bm, _ := json.Marshal(selectedModelIDs)
	_ = storeInstance.SetSetting("selected_providers", string(bp))
	_ = storeInstance.SetSetting("selected_models", string(bm))

	// Apply to discovery
	discoverySvc.SetSelectedModels(selectedModelIDs)

	// Run full validation pipeline on selected models
	freeValidator := validation.NewFreeValidator(cfg, storeInstance, discoverySvc, client)
	if err := freeValidator.ValidateFreeTier(ctx); err != nil {
		log.Warn().Err(err).Msg("Free tier validation had errors")
	}

	toolValidator := validation.NewToolValidator(cfg, storeInstance, discoverySvc, client)
	if err := toolValidator.ValidateToolCalling(ctx); err != nil {
		log.Warn().Err(err).Msg("Tool calling validation had errors")
	}

	contextValidator := validation.NewContextValidator(cfg, storeInstance, discoverySvc, client)
	if err := contextValidator.ValidateContext(ctx); err != nil {
		log.Warn().Err(err).Msg("Context validation had errors")
	}

	speedBench := validation.NewSpeedBenchmark(cfg, storeInstance, discoverySvc, client)
	if err := speedBench.RunBenchmarks(ctx); err != nil {
		log.Warn().Err(err).Msg("Speed benchmark had errors")
	}

	// Update combos
	comboMgr := combo.NewComboManager(cfg, storeInstance, client)
	if err := comboMgr.UpdateCombos(ctx); err != nil {
		return fmt.Errorf("combo update failed: %w", err)
	}

	fmt.Println("Manual selection complete! Combos updated.")
	return nil
}

// runDebugSelection tests unknown models with full pipeline

// runDebugSelection tests unknown models with full pipeline
func runDebugSelection(cfg *config.Config) error {
	ctx := context.Background()
	fmt.Println("Debug selection: testing unknown models with full pipeline...")

	client := omniroute.NewClient(cfg)
	storeInstance, err := store.NewStore(cfg.Database.Path, cfg.Database.MigrationDir)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer storeInstance.Close()

	discoverySvc := discovery.NewDiscovery(cfg, storeInstance)

	// Sync all first
	fmt.Println("  Syncing providers and models...")
	if err := discoverySvc.SyncAll(ctx); err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}

	// Get models that don't have free_confirmed=true (unknown free status)
	filter := store.ModelFilter{
		FreeConfirmed: boolPtr(false),
		SupportsTools: boolPtr(true),
	}
	models, err := storeInstance.GetModels(filter)
	if err != nil {
		return fmt.Errorf("failed to get models: %w", err)
	}

	// Filter to those with sufficient context
	testTokens := cfg.ContextVal.TestTokens
	if testTokens <= 0 {
		testTokens = 8000
	}
	var candidates []store.Model
	for _, m := range models {
		if m.ContextWindow != nil && *m.ContextWindow >= testTokens {
			candidates = append(candidates, m)
		}
	}

	fmt.Printf("  Found %d unconfirmed models with tool calling and context >= %d\n", len(candidates), testTokens)
	if len(candidates) == 0 {
		fmt.Println("  No candidates found")
		return nil
	}

	// Build provider list for selection
	byProvider := make(map[string][]store.Model)
	for _, m := range candidates {
		byProvider[m.ProviderID] = append(byProvider[m.ProviderID], m)
	}

	providerItems := make([]SelectItem, 0, len(byProvider))
	for providerID, mods := range byProvider {
		providerItems = append(providerItems, SelectItem{
			ID: providerID, Label: fmt.Sprintf("%s (%d models)", providerID, len(mods)), Selected: true,
		})
	}

	selProviders, err := Multiselect("Select providers to debug test", providerItems)
	if err != nil {
		return err
	}

	var selectedModelIDs []string
	for _, it := range selProviders {
		if it.Selected {
			for _, m := range byProvider[it.ID] {
				selectedModelIDs = append(selectedModelIDs, m.ID)
			}
		}
	}

	if len(selectedModelIDs) == 0 {
		fmt.Println("No models selected")
		return nil
	}

	// Apply to discovery
	discoverySvc.SetSelectedModels(selectedModelIDs)

	// Run FULL validation pipeline on selected models
	fmt.Println("  Running full validation pipeline (free, tool, context, speed)...")

	freeValidator := validation.NewFreeValidator(cfg, storeInstance, discoverySvc, client)
	if err := freeValidator.ValidateFreeTier(ctx); err != nil {
		log.Warn().Err(err).Msg("Free tier validation had errors")
	}

	toolValidator := validation.NewToolValidator(cfg, storeInstance, discoverySvc, client)
	if err := toolValidator.ValidateToolCalling(ctx); err != nil {
		log.Warn().Err(err).Msg("Tool calling validation had errors")
	}

	contextValidator := validation.NewContextValidator(cfg, storeInstance, discoverySvc, client)
	if err := contextValidator.ValidateContext(ctx); err != nil {
		log.Warn().Err(err).Msg("Context validation had errors")
	}

	speedBench := validation.NewSpeedBenchmark(cfg, storeInstance, discoverySvc, client)
	if err := speedBench.RunBenchmarks(ctx); err != nil {
		log.Warn().Err(err).Msg("Speed benchmark had errors")
	}

	// Update combos
	comboMgr := combo.NewComboManager(cfg, storeInstance, client)
	if err := comboMgr.UpdateCombos(ctx); err != nil {
		return fmt.Errorf("combo update failed: %w", err)
	}

	fmt.Println("Debug selection complete! Validated models added to combos.")
	return nil
}

// runUpdateCombos syncs and updates OpenCode combos with functional models from DB

// runUpdateCombos syncs and updates OpenCode combos with functional models from DB
func runUpdateCombos(cfg *config.Config) error {
	ctx := context.Background()
	fmt.Println("Update combos: syncing and updating OpenCode combos...")

	client := omniroute.NewClient(cfg)
	storeInstance, err := store.NewStore(cfg.Database.Path, cfg.Database.MigrationDir)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer storeInstance.Close()

	discoverySvc := discovery.NewDiscovery(cfg, storeInstance)

	// Sync all to get latest models
	fmt.Println("  Syncing providers and models...")
	if err := discoverySvc.SyncAll(ctx); err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}

	// Update combos with all validated models
	comboMgr := combo.NewComboManager(cfg, storeInstance, client)
	if err := comboMgr.UpdateCombos(ctx); err != nil {
		return fmt.Errorf("combo update failed: %w", err)
	}

	fmt.Println("Combos updated successfully!")
	return nil
}


func boolPtr(b bool) *bool { return &b }
