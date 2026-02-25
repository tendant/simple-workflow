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
	_ "github.com/lib/pq"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://pas:pwd@localhost/pas?sslmode=disable&schema=workflow"
	}

	// --- Producer: create a recurring schedule ---
	client, err := simpleworkflow.NewClient(dbURL)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	scheduleID, err := client.Schedule("report.weekly.v1", map[string]interface{}{
		"report_type": "sales_summary",
		"recipients":  []string{"team@example.com"},
	}).
		Cron("0 9 * * 1").                  // Every Monday at 9:00 AM
		InTimezone("America/New_York").      // Eastern time
		WithPriority(50).                    // Higher priority than default
		Create(context.Background())
	if err != nil {
		log.Fatalf("Failed to create schedule: %v", err)
	}
	log.Printf("Created schedule: %s", scheduleID)
	client.Close()

	// --- Worker: process report runs with embedded schedule ticker ---
	poller, err := simpleworkflow.NewPoller(dbURL)
	if err != nil {
		log.Fatalf("Failed to create poller: %v", err)
	}
	defer poller.Close()

	// Enable the schedule ticker so the poller also fires due schedules
	poller.WithScheduleTicker()

	poller.HandleFunc("report.weekly.v1", func(ctx context.Context, run *simpleworkflow.WorkflowRun) (interface{}, error) {
		var params struct {
			ReportType string   `json:"report_type"`
			Recipients []string `json:"recipients"`
		}
		if err := json.Unmarshal(run.Payload, &params); err != nil {
			return nil, fmt.Errorf("failed to parse payload: %w", err)
		}

		log.Printf("Generating %s report for %v", params.ReportType, params.Recipients)
		time.Sleep(3 * time.Second) // simulate report generation

		return map[string]interface{}{
			"status":      "sent",
			"report_type": params.ReportType,
			"recipients":  params.Recipients,
		}, nil
	})

	// Graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Shutting down...")
		cancel()
	}()

	log.Println("Starting scheduled report worker...")
	log.Println("Press Ctrl+C to stop")
	poller.Start(ctx)
}
