package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	simpleworkflow "github.com/tendant/simple-workflow"
	"github.com/tendant/simple-workflow/api"
)

func main() {
	addr := flag.String("addr", ":8080", "Listen address")
	dbURL := flag.String("db", "", "Database URL (postgres:// or sqlite://)")
	apiKey := flag.String("api-key", "", "API key for authentication (empty = disabled)")
	migrate := flag.Bool("migrate", false, "Run auto-migration on startup")
	flag.Parse()

	if *dbURL == "" {
		*dbURL = os.Getenv("DATABASE_URL")
	}
	if *dbURL == "" {
		log.Fatal("Database URL required: use --db flag or DATABASE_URL env var")
	}

	client, err := simpleworkflow.NewClient(*dbURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer client.Close()

	if *migrate {
		log.Println("Running auto-migration...")
		if err := client.AutoMigrate(context.Background()); err != nil {
			log.Fatalf("Migration failed: %v", err)
		}
		log.Println("Migration complete")
	}

	router := api.NewRouter(client, *apiKey)

	srv := &http.Server{
		Addr:         *addr,
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("Shutting down server...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	log.Printf("Starting server on %s", *addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}
