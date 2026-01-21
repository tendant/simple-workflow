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

// InvoiceExecutor generates invoices
type InvoiceExecutor struct{}

func (e *InvoiceExecutor) Execute(ctx context.Context, run *simpleworkflow.WorkflowRun) (interface{}, error) {
	var params struct {
		InvoiceID string  `json:"invoice_id"`
		Amount    float64 `json:"amount"`
		CustomerID string `json:"customer_id"`
	}

	if err := json.Unmarshal(run.Payload, &params); err != nil {
		return nil, fmt.Errorf("failed to parse payload: %w", err)
	}

	log.Printf("Generating invoice %s for customer %s (amount: $%.2f)",
		params.InvoiceID, params.CustomerID, params.Amount)

	// Simulate invoice generation
	time.Sleep(3 * time.Second)

	log.Printf("Invoice %s generated successfully", params.InvoiceID)

	return map[string]interface{}{
		"status":     "generated",
		"invoice_id": params.InvoiceID,
		"pdf_url":    fmt.Sprintf("https://invoices.example.com/%s.pdf", params.InvoiceID),
	}, nil
}

// PaymentExecutor processes payments
type PaymentExecutor struct{}

func (e *PaymentExecutor) Execute(ctx context.Context, run *simpleworkflow.WorkflowRun) (interface{}, error) {
	var params struct {
		PaymentID string  `json:"payment_id"`
		Amount    float64 `json:"amount"`
		Method    string  `json:"method"`
	}

	if err := json.Unmarshal(run.Payload, &params); err != nil {
		return nil, fmt.Errorf("failed to parse payload: %w", err)
	}

	log.Printf("Processing payment %s (amount: $%.2f, method: %s)",
		params.PaymentID, params.Amount, params.Method)

	// Simulate payment processing
	time.Sleep(2 * time.Second)

	// Simulate occasional failures for retry demonstration
	if params.Amount > 10000 && run.Attempt == 0 {
		return nil, fmt.Errorf("payment gateway timeout (will retry)")
	}

	log.Printf("Payment %s processed successfully", params.PaymentID)

	return map[string]interface{}{
		"status":        "completed",
		"payment_id":    params.PaymentID,
		"transaction_id": fmt.Sprintf("txn_%s", params.PaymentID),
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

	// Create poller for ALL billing workflows using type-prefix routing
	config := simpleworkflow.PollerConfig{
		TypePrefixes:  []string{"billing.%"}, // Matches billing.invoice.v1, billing.payment.v1, etc.
		LeaseDuration: 30 * time.Second,
		PollInterval:  2 * time.Second,
		WorkerID:      "billing-worker-go-1",
	}

	poller := simpleworkflow.NewPoller(db, config)

	// Register executors for specific workflow types
	poller.RegisterExecutor("billing.invoice.v1", &InvoiceExecutor{})
	poller.RegisterExecutor("billing.payment.v1", &PaymentExecutor{})

	// Start poller in background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		log.Println("Starting billing worker (handles all billing.* workflows)...")
		poller.Start(ctx)
	}()

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down worker...")
	cancel()
	time.Sleep(time.Second)
	log.Println("Worker stopped")
}
