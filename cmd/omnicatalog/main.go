// Package main is the entry point for omnicatalog.
// OmniRoute model validator for OpenCode - runs in background, minimal RAM usage.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"omnicatalog/internal/cli"
	"omnicatalog/internal/config"
)

var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

func main() {
	// Initial logger setup (before loading config)
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Capture signals for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Info().Msg("Shutdown signal received, exiting...")
		cancel()
	}()

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load configuration")
	}

	// Adjust log level based on config
	level, err := zerolog.ParseLevel(cfg.Logging.Level)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)

	if cfg.Logging.Format == "json" {
		log.Logger = log.Output(os.Stderr)
	}

	log.Info().
		Str("version", version).
		Str("commit", commit).
		Str("build_date", buildDate).
		Msg("Starting omnicatalog")

	// Execute CLI command
	rootCmd := cli.NewRootCommand(cfg, version, commit, buildDate)
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		log.Fatal().Err(err).Msg("Command execution error")
	}
}