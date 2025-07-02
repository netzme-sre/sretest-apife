package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"runtime"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/segmentio/kafka-go"
)

var (
	activeRequests int32
	kafkaWriter    *kafka.Writer
	invoiceQueue   chan Invoice
	workerCount    int
	queueCapacity  int
	workerWG       sync.WaitGroup
)

type Config struct {
	listenAddress string
	InvoiceDelay  time.Duration
	EnableDrain   bool
	KafkaBrokers  string
	KafkaTopic    string
}

func LoadConfig() (*Config, error) {
	if err := godotenv.Load(); err != nil {
		return nil, err
	}

	invoiceDelay, err := time.ParseDuration(os.Getenv("INVOICE_DELAY"))
	if err != nil {
		invoiceDelay = 0 // Default to no delay if not set or invalid
	}

	enableDrain, _ := strconv.ParseBool(os.Getenv("ENABLE_DRAIN"))

	return &Config{
		listenAddress: os.Getenv("SERVER_LISTEN_ADDRESS"),
		InvoiceDelay:  invoiceDelay,
		EnableDrain:   enableDrain,
		KafkaBrokers:  os.Getenv("KAFKA_BROKERS"),
		KafkaTopic:    os.Getenv("KAFKA_TOPIC"),
	}, nil
}

type Invoice struct {
	MerchantName string    `json:"merchant_name"`
	InvoiceID    string    `json:"invoice_id,omitempty"`
	Price        float64   `json:"price"`
	CreateAt     time.Time `json:"create_at,omitempty"`
}

func CreateInvoice(w http.ResponseWriter, r *http.Request, cfg *Config) {
	atomic.AddInt32(&activeRequests, 1)
	defer atomic.AddInt32(&activeRequests, -1)
	time.Sleep(cfg.InvoiceDelay)
	var invoice Invoice
	if err := json.NewDecoder(r.Body).Decode(&invoice); err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		return
	}

	invoice.InvoiceID = uuid.New().String()
	invoice.CreateAt = time.Now().UTC()

	select {
	case invoiceQueue <- invoice:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "queued", "invoice_id": invoice.InvoiceID})
	default:
		http.Error(w, "Server busy, try again later", http.StatusServiceUnavailable)
	}
}

// Worker function to process invoices from the queue and write to Kafka
func invoiceWorker() {
	workerWG.Add(1)
	defer workerWG.Done()
	for invoice := range invoiceQueue {
		msgBytes, err := json.Marshal(invoice)
		if err != nil {
			log.Printf("Failed to encode invoice: %v", err)
			continue
		}
		err = kafkaWriter.WriteMessages(context.Background(), kafka.Message{
			Value: msgBytes,
		})
		if err != nil {
			log.Printf("Failed to write to Kafka: %v", err)
			continue
		}
		log.Printf("Produced Invoice to Kafka - InvoiceID: %s, Merchant: %s, Price: %.2f, CreateAt: %s\n",
			invoice.InvoiceID, invoice.MerchantName, invoice.Price, invoice.CreateAt.Format(time.RFC3339))
	}
}

func readinessProbe(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	err := kafkaWriter.WriteMessages(ctx, kafka.Message{
		Value: []byte("healthcheck"),
	})
	if err != nil {
		http.Error(w, "Kafka is not ready", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func livenessProbe(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func init() {
	// Detect CPU cores (works in containers if limits are set)
	cpuCores := runtime.NumCPU()

	// Use multipliers, allow override via env
	workerMultiplier := 2
	queueMultiplier := 10
	expectedRPS := 1000

	if val, ok := os.LookupEnv("WORKER_MULTIPLIER"); ok {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			workerMultiplier = parsed
		}
	}
	if val, ok := os.LookupEnv("QUEUE_MULTIPLIER"); ok {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			queueMultiplier = parsed
		}
	}
	if val, ok := os.LookupEnv("EXPECTED_RPS"); ok {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			expectedRPS = parsed
		}
	}

	workerCount = cpuCores * workerMultiplier
	queueCapacity = expectedRPS * queueMultiplier

	// Allow direct override
	if val, ok := os.LookupEnv("WORKER_COUNT"); ok {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			workerCount = parsed
		}
	}
	if val, ok := os.LookupEnv("QUEUE_CAPACITY"); ok {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			queueCapacity = parsed
		}
	}
}

func main() {
	// Set log output to stdout
	log.SetOutput(os.Stdout)

	log.Printf("Auto workerCount=%d, queueCapacity=%d (cpu=%d)", workerCount, queueCapacity, runtime.NumCPU())

	// Load configuration
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatal(err)
	}

	// Initialize Kafka writer
	kafkaWriter = &kafka.Writer{
		Addr:     kafka.TCP(cfg.KafkaBrokers),
		Topic:    cfg.KafkaTopic,
		Balancer: &kafka.LeastBytes{},
	}

	// Initialize invoice queue and start workers
	invoiceQueue = make(chan Invoice, queueCapacity)
	for i := 0; i < workerCount; i++ {
		go invoiceWorker()
	}

	// Setup HTTP routes
	http.HandleFunc("/live", livenessProbe)
	http.HandleFunc("/ready", readinessProbe)
	http.HandleFunc("/invoice", func(w http.ResponseWriter, r *http.Request) {
		CreateInvoice(w, r, cfg)
	})

	// Create a server instance
	server := &http.Server{
		Addr: cfg.listenAddress,
	}

	// Handle graceful shutdown
	go func() {
		log.Println("Starting server on", cfg.listenAddress)
		log.Println("Ready to receive data. Kafka connected.")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	// Graceful shutdown on SIGTERM
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit

	log.Println("Shutting down server...")

	// Set a deadline to allow pending requests to complete
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if cfg.EnableDrain {
		done := make(chan bool)
		go func() {
			for atomic.LoadInt32(&activeRequests) > 0 {
				log.Printf("Waiting for %d active requests to finish...", atomic.LoadInt32(&activeRequests))
				time.Sleep(500 * time.Millisecond)
			}
			done <- true
		}()

		select {
		case <-done:
			log.Println("All active requests are done, safe to shutdown.")
		case <-ctx.Done():
			log.Println("Timeout reached. Some requests may not have completed.")
		}
	} else {
		remainingRequests := atomic.LoadInt32(&activeRequests)
		log.Printf("Drain process is disabled. Shutting down immediately. %d active requests will be interrupted.", remainingRequests)
	}

	close(invoiceQueue)
	workerWG.Wait()
	log.Println("All invoice workers have finished processing the queue.")

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown. There are %d active requests remaining.", atomic.LoadInt32(&activeRequests))
		log.Fatal(err)
	}

	log.Println("Server stopped.")
	log.Printf("There are %d active requests remaining.", atomic.LoadInt32(&activeRequests))
}
