package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Inkbinder/autopilot/internal/orchestrator"
	"github.com/Inkbinder/autopilot/internal/runstate"
	"github.com/Inkbinder/autopilot/internal/workflow"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func run() error {
	var port int
	flag.IntVar(&port, "port", -1, "HTTP status server port override")
	flag.Parse()

	workflowPath := "./WORKFLOW.md"
	if flag.NArg() > 1 {
		return fmt.Errorf("expected at most one positional workflow path")
	}
	if flag.NArg() == 1 {
		workflowPath = flag.Arg(0)
	}
	absWorkflowPath, err := filepath.Abs(workflowPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(absWorkflowPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s: %w", workflow.ErrMissingWorkflowFile, err)
		}
		return err
	}
	var portOverride *int
	if port >= 0 {
		portValue := port
		portOverride = &portValue
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	_, config, err := workflow.LoadAndResolve(absWorkflowPath, nil)
	if err != nil {
		return err
	}
	telemetryShutdown, err := configureGlobalTelemetry(context.Background(), config)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetryShutdown(shutdownCtx); err != nil {
			logger.Warn("telemetry shutdown failed", slog.Any("error", err))
		}
	}()
	runStore, err := runstate.OpenSQLite(filepath.Join(filepath.Dir(absWorkflowPath), ".autopilot", "runs.db"))
	if err != nil {
		return err
	}
	defer runStore.Close()
	service, err := orchestrator.New(absWorkflowPath, orchestrator.Options{Logger: logger, PortOverride: portOverride, RunStore: runStore})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return service.Run(ctx)
}
