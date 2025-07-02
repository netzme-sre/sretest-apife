# sre-create-invoice-apife

A Go microservice for accepting invoice creation requests via HTTP, queuing them, and asynchronously producing them to Kafka. Designed for high-throughput, scalable, and resilient invoice ingestion.

> **Warning**  
> This service is **not secure** and is intended **only for testing or demonstration purposes**.  
> Do not use in production environments without adding authentication, authorization, and proper input validation.

## Features

- **HTTP API** for invoice creation (`/invoice`)
- **Asynchronous processing**: requests are queued and processed by a worker pool
- **Kafka integration**: invoices are produced to a Kafka topic
- **Configurable worker pool and queue size** via environment variables
- **Health endpoints**: `/live` (liveness) and `/ready` (Kafka readiness)
- **Graceful shutdown** with optional draining of active requests

## Environment Variables

| Variable              | Description                                      | Example/Default         |
|-----------------------|--------------------------------------------------|-------------------------|
| SERVER_LISTEN_ADDRESS | HTTP listen address                              | `:9009`                |
| INVOICE_DELAY         | Artificial delay for invoice creation (e.g. `0`) | `0`                    |
| ENABLE_DRAIN          | Wait for active requests on shutdown             | `true`                 |
| KAFKA_BROKERS         | Kafka broker addresses (comma-separated)         | `localhost:9092`       |
| KAFKA_TOPIC           | Kafka topic to produce invoices to               | `sreinvoice`           |
| WORKER_MULTIPLIER     | Multiplier for CPU cores to set worker count     | `2`                    |
| QUEUE_MULTIPLIER      | Multiplier for expected RPS to set queue size    | `10`                   |
| EXPECTED_RPS          | Expected requests per second                     | `1000`                 |
| WORKER_COUNT          | Override worker count (optional)                 |                         |
| QUEUE_CAPACITY        | Override queue capacity (optional)               |                         |

> Optional tuning parameters for worker pool and queue.
> By default, these are automatically calculated based on CPU and expected RPS.
> You can override them by uncommenting and setting the values below:
> - WORKER_MULTIPLIER: Multiplies CPU cores to set worker count (default: 2)
> - QUEUE_MULTIPLIER: Multiplies EXPECTED_RPS to set queue capacity (default: 10)
> - EXPECTED_RPS: Expected requests per second (default: 1000)
> - WORKER_COUNT: Directly set the number of worker goroutines (overrides multiplier)
> - QUEUE_CAPACITY: Directly set the queue buffer size (overrides multiplier)
> Example: To handle higher load, increase multipliers or set explicit values.
> WORKER_MULTIPLIER=2
> QUEUE_MULTIPLIER=10
> EXPECTED_RPS=1000
> WORKER_COUNT=           # Uncomment and set to override auto worker count
> QUEUE_CAPACITY=         # Uncomment and set to override auto queue capacity

## API Endpoints

- `POST /invoice`  
  Accepts a JSON payload:
  ```json
  {
    "merchant_name": "string",
    "price": 123.45
  }
  ```
  Responds with:
  ```json
  {
    "invoice_id": "uuid-string",
    "status": "queued"
    
  }
  ```
  - Returns `202 Accepted` if queued, `503` if queue is full, `400` if invalid.

- `GET /live`  
  Liveness probe. Always returns `200 OK`.

- `GET /ready`  
  Readiness probe. Checks Kafka connectivity. Returns `200 OK` if ready, `503` if not.

## Example Usage

Create an invoice using `curl`:

```sh
curl -X POST http://localhost:9009/invoice \
  -H "Content-Type: application/json" \
  -d '{"merchant_name": "Acme Corp", "price": 99.99}'
```

Check liveness:

```sh
curl http://localhost:9009/live
```

Check readiness:

```sh
curl http://localhost:9009/ready
```

## Running Locally

1. Set environment variables as needed (see above).
2. Ensure Kafka is running and accessible.
3. Build and run:
    ```sh
    go build -o sre-create-invoice-apife
    ./sre-create-invoice-apife
    ```

## Graceful Shutdown

Send `SIGTERM` or `SIGINT` to the process.  
If `ENABLE_DRAIN=true`, the service waits for active requests and drains the queue before exiting.

## Worker Pool & Queue Sizing

- By default, the number of workers = `CPU cores * WORKER_MULTIPLIER`.
- Queue capacity = `EXPECTED_RPS * QUEUE_MULTIPLIER`.
- You can override both with `WORKER_COUNT` and `QUEUE_CAPACITY`.

---