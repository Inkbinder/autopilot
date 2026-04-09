package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"autopilot/internal/orchestrator"
	"autopilot/internal/workflow"
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
	service, err := orchestrator.New(absWorkflowPath, orchestrator.Options{PortOverride: portOverride})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return service.Run(ctx)
}