# EnergyGrid Data Aggregator

This is a client application designed to fetch, aggregate, and report telemetry data from the EnergyGrid Mock API. It focuses on handling strict rate limits and network instability gracefully.

## Prerequisites

- **Go** (1.18 or higher)
- **Node.js** (v14 or higher) - Required only for the mock server

## Getting Started

### 1. Start the Server

The mock server simulates the API behavior, including the 1 req/sec rate limit.

```bash
# Install dependencies
npm install

# Start the server (keep this terminal running)
node server.js
```

### 2. Run the Client

In a separate terminal, run the aggregator:

```bash
go run main.go
```

The application will process 500 devices in batches of 10. You should see progress logs appearing roughly once per second.

### Configuration

You can override the default API token using environment variables if needed:

```bash
export ENERGYGRID_TOKEN="your_custom_token"
go run main.go
```

## Running Tests

Unit tests are included for the core logic (signature generation, response parsing, and ID generation).

Run them with:
```bash
go test -v
```

## How It Works

### Concurrency & Rate Limiting
Instead of spawning 500 concurrent requests (which would immediately get rate-limited), I used a **Producer-Consumer** pattern:

- **Queue**: A buffered channel holds all 50 batches (500 serial numbers).
- **Worker**: A single worker reads from this queue.
- **Ticker**: A `time.Ticker` set to `1050ms` ensures we respect the 1 request/second limit. I added logic to fire the first request immediately, so we don't waste time waiting at startup.

### Robustness
- **Context Handling**: The API client respects `context.Context` for correct timeout handling.
- **Retries**: If the server returns a `429` (Rate Limit) or a network error, the client retries up to 3 times with exponential backoff.
- **Fault Tolerance**: A single failed batch is logged but doesn't crash the entire program.

### Security
Requests are signed using the required MD5 hash of `path + token + timestamp`. I verify this logic in `main_test.go` against a known MD5 hash to ensure correctness.

## Output

The final report is saved to `aggregated_report.json` and includes:
- Aggregated stats (Total Power, Success/Fail counts).
- A complete list of device telemetry.
