package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	simpleworkflow "github.com/tendant/simple-workflow"
)

func main() {
	// SQLite connection — only the connection string changes!
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "sqlite:///tmp/workflow.db"
	}

	// Create client (auto-detects SQLite from the connection string)
	client, err := simpleworkflow.NewClient(dbURL)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	// Auto-migrate creates tables (no goose needed for SQLite)
	if err := client.AutoMigrate(context.Background()); err != nil {
		log.Fatalf("Failed to migrate: %v", err)
	}

	// Submit a test workflow
	runID, err := client.Submit("demo.hello.v1", map[string]string{
		"message": "Hello from SQLite!",
	}).Execute(context.Background())
	if err != nil {
		log.Fatalf("Failed to submit workflow: %v", err)
	}
	log.Printf("Submitted workflow run: %s", runID)

	// Create a poller (also auto-detects SQLite)
	poller, err := simpleworkflow.NewPoller(dbURL)
	if err != nil {
		log.Fatalf("Failed to create poller: %v", err)
	}
	defer poller.Close()

	poller.HandleFunc("demo.hello.v1", func(ctx context.Context, run *simpleworkflow.WorkflowRun) (interface{}, error) {
		var params map[string]string
		if err := json.Unmarshal(run.Payload, &params); err != nil {
			return nil, fmt.Errorf("failed to unmarshal payload: %w", err)
		}
		log.Printf("Processing: %s", params["message"])
		time.Sleep(100 * time.Millisecond)
		return map[string]string{"status": "done"}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go poller.Start(ctx)

	// Wait for signal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("Shutting down...")
	poller.Stop()
}
