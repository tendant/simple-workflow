package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	simpleworkflow "github.com/tendant/simple-workflow"
	_ "github.com/lib/pq"
)

func main() {
	// Database connection URL
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://pas:pwd@localhost/pas?sslmode=disable&schema=workflow"
	}

	// Create poller with simplified API
	poller, err := simpleworkflow.NewPoller(dbURL)
	if err != nil {
		log.Fatalf("Failed to create poller: %v", err)
	}
	defer poller.Close()

	// Register handler using HandleFunc (no need for executor struct)
	poller.HandleFunc("content.thumbnail.v1", func(ctx context.Context, run *simpleworkflow.WorkflowRun) (interface{}, error) {
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
	})

	// Optional: Set up Prometheus metrics
	// metrics := simpleworkflow.NewPrometheusMetrics(nil)
	// poller.SetMetrics(metrics)

	// Start poller (blocks until interrupted)
	log.Println("Starting thumbnail worker...")
	log.Println("Press Ctrl+C to stop")
	poller.Start(context.Background())
}
