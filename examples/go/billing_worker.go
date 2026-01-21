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

	// Register invoice handler
	poller.HandleFunc("billing.invoice.v1", func(ctx context.Context, run *simpleworkflow.WorkflowRun) (interface{}, error) {
		var params struct {
			InvoiceID  string  `json:"invoice_id"`
			Amount     float64 `json:"amount"`
			CustomerID string  `json:"customer_id"`
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
	})

	// Register payment handler
	poller.HandleFunc("billing.payment.v1", func(ctx context.Context, run *simpleworkflow.WorkflowRun) (interface{}, error) {
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
			"status":         "completed",
			"payment_id":     params.PaymentID,
			"transaction_id": fmt.Sprintf("txn_%s", params.PaymentID),
		}, nil
	})

	// Start poller (blocks until interrupted)
	log.Println("Starting billing worker (handles all billing.* workflows)...")
	log.Println("Press Ctrl+C to stop")
	poller.Start(context.Background())
}
