# EnergyGrid Data Aggregator

This is a robust client application to fetch, aggregate, and report telemetry data from the EnergyGrid Mock API, adhering to strict rate limits and security protocols.

## Prerequisites

- **Go** (1.18 or higher)
- **Node.js** (v14 or higher) for the mock server

## Setup and Run

### 1. Start the Mock Server

The mock server simulates the EnergyGrid API with strict rate limits (1 req/sec).

```bash
# Install dependencies
npm install

# Start the server
node server.js
```
*Keep this terminal open.*

### 2. Run the Aggregator Client

Open a new terminal window and run the Go application:

```bash
go run main.go
```

## Implementation Details

### Approach

The solution uses a **Producer-Consumer (Worker)** pattern to handle concurrency and strict rate limiting efficiently.

- **Queue System**: A buffered channel (`jobs`) holds batches of serial numbers.
- **Worker**: A single worker goroutine consumes jobs from the queue.
- **Rate Limiting**: A `time.Ticker` is used within the worker to strictly enforce a >1 second delay (`1050ms`) between requests. This is more robust than `time.Sleep` as it guarantees the interval regardless of execution time, preventing drift while adhering to the API contract.
- **Batching**: Serial numbers are chunked into batches of 10 (API limit) before being sent to the queue.
- **Security**: The `Signature` header is dynamically generated for each request using `MD5(URL Path + Token + Timestamp)`.

### Robustness

- **Retries**: The client implements an exponential backoff retry mechanism (up to 3 attempts) for handling `429 Too Many Requests` or transient network errors.
- **Error Handling**: Failed batches are tracked, but they do not stop the aggregation process. The final report distinguishes between successful and failed devices.

## Output

After execution, the program generates `aggregated_report.json` containing:
- Summary statistics (Total/Average Power).
- Detailed telemetry for all 500 devices.
