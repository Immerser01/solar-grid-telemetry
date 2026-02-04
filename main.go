package main

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	apiBaseURL   = "http://localhost:3000"
	apiEndpoint  = "/device/real/query"
	token        = "interview_token_123"
	batchSize    = 10
	rateLimit    = 1050 * time.Millisecond // Slightly over 1s to be safe
	maxRetries   = 3
	retryDelay   = 2 * time.Second
	totalDevices = 500
)

// RawDeviceData represents the raw response from the server
type RawDeviceData struct {
	Sn          string `json:"sn"`
	Power       string `json:"power"`  // "2.5 kW"
	Status      string `json:"status"` // "Online" | "Offline"
	LastUpdated string `json:"last_updated"`
}

// DeviceData represents the clean data for our report
type DeviceData struct {
	SerialNumber string    `json:"serialNumber"`
	Power        float64   `json:"power"`
	Status       string    `json:"status"`
	LastUpdated  time.Time `json:"lastUpdated"`
}

// APIResponse represents the wrapper object { "data": [...] }
type APIResponse struct {
	Data  []RawDeviceData `json:"data"`
	Error string          `json:"error,omitempty"`
}

// AggregatedReport represents the final aggregated result
type AggregatedReport struct {
	TotalDevices      int          `json:"totalDevices"`
	SuccessfulDevices int          `json:"successfulDevices"`
	FailedDevices     int          `json:"failedDevices"`
	TotalPower        float64      `json:"totalPower"`
	AveragePower      float64      `json:"averagePower"`
	DeviceData        []DeviceData `json:"deviceData"`
}

// generateSerialNumbers generates dummy serial numbers
func generateSerialNumbers() []string {
	serials := make([]string, totalDevices)
	for i := 0; i < totalDevices; i++ {
		serials[i] = fmt.Sprintf("SN-%03d", i)
	}
	return serials
}

// generateSignature creates MD5 signature: MD5(url_path + token + timestamp)
func generateSignature(urlPath, token, timestamp string) string {
	data := urlPath + token + timestamp
	hash := md5.Sum([]byte(data))
	return fmt.Sprintf("%x", hash)
}

// parsePower string "2.5 kW" -> 2.5
func parsePower(p string) float64 {
	parts := strings.Split(p, " ")
	if len(parts) > 0 {
		val, err := strconv.ParseFloat(parts[0], 64)
		if err == nil {
			return val
		}
	}
	return 0.0
}

// fetchBatch fetches data for a single batch of serial numbers
func fetchBatch(client *http.Client, batch []string) ([]DeviceData, error) {
	timestamp := fmt.Sprintf("%d", time.Now().UnixMilli()) // Using ms for precision, though server might expect sec. Server uses Date.now() which is ms, wait... checking server.js... md5(url+token+timestamp). The timestamp comes from header.
	// Actually server.js just concats timestamp. Let's use string.
	// Server signature check:
	// const timestamp = req.headers["timestamp"];
	// const expectedSig = crypto.createHash("md5").update(url + SECRET_TOKEN + timestamp).digest("hex");
	// Standard practice is usually seconds but JS Date.now() is ms. Let's try standard timestamp string.
	// Wait, if I use time.Now().Unix(), it's seconds. If server uses Date.now() for "now" checking logic (rate limit), that is fine.
	// For signature, it just verifies strings match.
	timestamp = fmt.Sprintf("%d", time.Now().UnixMilli())

	signature := generateSignature(apiEndpoint, token, timestamp)

	payload := map[string]interface{}{
		"sn_list": batch,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal error: %w", err)
	}

	fullURL := apiBaseURL + apiEndpoint
	req, err := http.NewRequest("POST", fullURL, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("request create error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Signature", signature)
	req.Header.Set("Timestamp", timestamp)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("rate limited (429)")
	}

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("api error %d: %s", resp.StatusCode, string(b))
	}

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("decode error: %w", err)
	}

	var devices []DeviceData
	for _, raw := range apiResp.Data {
		parsedTime, _ := time.Parse(time.RFC3339, raw.LastUpdated)
		devices = append(devices, DeviceData{
			SerialNumber: raw.Sn,
			Power:        parsePower(raw.Power),
			Status:       raw.Status,
			LastUpdated:  parsedTime,
		})
	}

	return devices, nil
}

// processWithQueue manages the fetching process using a rate-limited queue
func processWithQueue(serials []string) *AggregatedReport {
	// Channels
	type Job struct {
		BatchID int
		Batch   []string
	}
	type Result struct {
		Data  []DeviceData
		Error error
		Batch []string // for reporting failed devices
	}

	// Calculate number of batches
	numBatches := (len(serials) + batchSize - 1) / batchSize
	jobs := make(chan Job, numBatches)
	results := make(chan Result, numBatches)

	// Fill jobs
	for i := 0; i < len(serials); i += batchSize {
		end := i + batchSize
		if end > len(serials) {
			end = len(serials)
		}
		jobs <- Job{
			BatchID: i/batchSize + 1,
			Batch:   serials[i:end],
		}
	}
	close(jobs)

	// Worker
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		client := &http.Client{Timeout: 10 * time.Second}
		ticker := time.NewTicker(rateLimit)
		defer ticker.Stop()

		for job := range jobs {
			// Strict rate limit: Block until tick
			<-ticker.C

			log.Printf("Processing Batch %d/%d (%d devices)...", job.BatchID, numBatches, len(job.Batch))

			var data []DeviceData
			var err error

			// Simple exponential backoff retry within the worker step
			// We do NOT want to release the token (tick) during retries to properly space out *our* requests,
			// OR we want to sleep.
			// Since we have a single worker, sleeping here effectively delays the next batch, which is what we want.
			for attempt := 0; attempt < maxRetries; attempt++ {
				data, err = fetchBatch(client, job.Batch)
				if err == nil {
					break
				}

				// If 429, we *must* wait. If network error, we also wait.
				log.Printf("Batch %d attempt %d failed: %v. Retrying...", job.BatchID, attempt+1, err)
				time.Sleep(retryDelay * time.Duration(attempt+1))
			}

			results <- Result{Data: data, Error: err, Batch: job.Batch}
		}
	}()

	// Wait for worker to finish in a separate goroutine to close results
	go func() {
		wg.Wait()
		close(results)
	}()

	// Aggregate results
	report := &AggregatedReport{
		TotalDevices: len(serials),
		DeviceData:   make([]DeviceData, 0),
	}

	for res := range results {
		if res.Error != nil {
			log.Printf("Failed batch: %v", res.Error)
			report.FailedDevices += len(res.Batch)
		} else {
			report.SuccessfulDevices += len(res.Data)
			report.DeviceData = append(report.DeviceData, res.Data...)
		}
	}

	// Calculate Stats
	if report.SuccessfulDevices > 0 {
		var totalPower float64
		for _, d := range report.DeviceData {
			totalPower += d.Power
		}
		report.TotalPower = totalPower
		report.AveragePower = totalPower / float64(report.SuccessfulDevices)
	}

	return report
}

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Println("=== EnergyGrid Data Aggregator ===")

	// Ensure URL is valid
	if _, err := url.Parse(apiBaseURL); err != nil {
		log.Fatalf("Invalid API URL: %v", err)
	}

	serials := generateSerialNumbers()

	// Start timer
	start := time.Now()
	report := processWithQueue(serials)
	duration := time.Since(start)

	// Summary
	log.Println("\n=== Aggregation Complete ===")
	log.Printf("Total: %d | Success: %d | Failed: %d", report.TotalDevices, report.SuccessfulDevices, report.FailedDevices)
	log.Printf("Total Power: %.2f kW", report.TotalPower)
	log.Printf("Duration: %v", duration)

	// Save
	bytes, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile("aggregated_report.json", bytes, 0644); err != nil {
		log.Fatalf("Failed to write report: %v", err)
	}
	log.Println("Report saved to aggregated_report.json")
}
