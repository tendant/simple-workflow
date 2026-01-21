package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	simpleworkflow "github.com/tendant/simple-workflow"
	_ "github.com/lib/pq"
)

// ThumbnailExecutor generates image thumbnails
type ThumbnailExecutor struct{}

func (e *ThumbnailExecutor) Execute(ctx context.Context, run *simpleworkflow.WorkflowRun) (interface{}, error) {
	var params struct {
		ContentID string `json:"content_id"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	}

	if err := json.Unmarshal(run.Payload, &params); err != nil {
		return nil, fmt.Errorf("failed to parse payload: %w", err)
	}

	log.Printf("Generating thumbnail for content %s (%dx%d)", params.ContentID, params.Width, params.Height)

	// Simulate thumbnail generation
	time.Sleep(2 * time.Second)

	log.Printf("Thumbnail completed for content %s", params.ContentID)

	return map[string]interface{}{
		"status":     "completed",
		"content_id": params.ContentID,
		"url":        fmt.Sprintf("https://cdn.example.com/%s_thumb_%dx%d.jpg", params.ContentID, params.Width, params.Height),
	}, nil
}

func main() {
	// Database connection
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://pas:pwd@localhost/pas?sslmode=disable&search_path=workflow"
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	// Create poller with configuration
	config := simpleworkflow.PollerConfig{
		TypePrefixes:  []string{"content.thumbnail.%"},
		LeaseDuration: 30 * time.Second,
		PollInterval:  2 * time.Second,
		WorkerID:      "thumbnail-worker-go-1",
	}

	poller := simpleworkflow.NewPoller(db, config)

	// Register executor
	poller.RegisterExecutor("content.thumbnail.v1", &ThumbnailExecutor{})

	// Optional: Set up Prometheus metrics
	// metrics := simpleworkflow.NewPrometheusMetrics(nil)
	// poller.SetMetrics(metrics)

	// Start poller in background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		log.Println("Starting thumbnail worker...")
		poller.Start(ctx)
	}()

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down worker...")
	cancel()
	time.Sleep(time.Second) // Allow graceful shutdown
	log.Println("Worker stopped")
}
