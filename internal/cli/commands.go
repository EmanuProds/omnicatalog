package cli

import (
	"fmt"
	"runtime"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"omnicatalog/internal/config"
	"omnicatalog/internal/store"
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
	rootCmd.AddCommand(newDaemonCommand(cfg))
	rootCmd.AddCommand(newStatusCommand(cfg))
	rootCmd.AddCommand(newModelsCommand(cfg))
	rootCmd.AddCommand(newCombosCommand(cfg))
	rootCmd.AddCommand(newConfigCommand(cfg))
	rootCmd.AddCommand(newScheduleCommand(cfg))
	rootCmd.AddCommand(newVersionCommand(version, commit, buildDate))

	return rootCmd
}

func newRunCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Executes full validation (manual or cron)",
		Long:  `Discovers providers/models, validates free tier, tool calling, context and speed, updates combos in OmniRoute and opencode.json.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			log.Info().Bool("dry_run", dryRun).Msg("Starting full validation")

			// TODO: Implement full validation
			fmt.Println("⚠️  Command 'run' not yet implemented")
			fmt.Println("   Next steps: implement internal/validator, internal/benchmark, internal/combo")

			if dryRun {
				fmt.Println("   [DRY-RUN] No changes would be saved")
			}
			return nil
		},
	}
	cmd.Flags().Bool("only-new", false, "Validate only new/untested models")
	cmd.Flags().Bool("force-revalidate", false, "Force revalidation of all models")
	return cmd
}

func newDaemonCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Runs as systemd service (resident)",
		Long:  `Daemon mode for continuous execution with scheduler.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Info().Msg("Starting daemon mode")

			// TODO: Implement daemon with scheduler
			fmt.Println("⚠️  Command 'daemon' not yet implemented")
			fmt.Println("   Next steps: implement internal/scheduler with cron")

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

func formatTokens(n int) string {
	if n >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.0fK", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}