package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	simpleworkflow "github.com/tendant/simple-workflow"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "sqlite:///tmp/workflow.db"
	}

	// All-in-one: producer + worker in a single object
	wf, err := simpleworkflow.New(dbURL)
	if err != nil {
		log.Fatalf("Failed to create workflow: %v", err)
	}
	defer wf.Close()

	// Create tables (no goose needed for SQLite)
	if err := wf.AutoMigrate(context.Background()); err != nil {
		log.Fatalf("Failed to migrate: %v", err)
	}

	// Register a handler
	wf.HandleFunc("demo.hello.v1", func(ctx context.Context, run *simpleworkflow.WorkflowRun) (any, error) {
		var params map[string]string
		if err := json.Unmarshal(run.Payload, &params); err != nil {
			return nil, fmt.Errorf("failed to unmarshal payload: %w", err)
		}
		log.Printf("Processing: %s", params["message"])
		return map[string]string{"status": "done"}, nil
	})

	// Submit a test workflow
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runID, err := wf.Submit("demo.hello.v1", map[string]string{
		"message": "Hello from SQLite!",
	}).Execute(ctx)
	if err != nil {
		log.Fatalf("Failed to submit workflow: %v", err)
	}
	log.Printf("Submitted workflow run: %s", runID)

	// Start polling
	go wf.Start(ctx)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("Shutting down...")
	wf.Stop()
}
