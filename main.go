package main

import (
	"bytes"
	"context"
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

// Configuration constants
const (
	apiBaseURL   = "http://localhost:3000"
	apiEndpoint  = "/device/real/query"
	defaultToken = "interview_token_123"
	batchSize    = 10
	// 1050ms allows a safe buffer over the 1s strict limit
	rateLimit    = 1050 * time.Millisecond
	maxRetries   = 3
	retryDelay   = 2 * time.Second
	totalDevices = 500
)

// getToken reads the API token from environment or falls back to default
func getToken() string {
	if token := os.Getenv("ENERGYGRID_TOKEN"); token != "" {
		return token
	}
	return defaultToken
}

// Server response structures

type RawDeviceData struct {
	Sn          string `json:"sn"`
	Power       string `json:"power"` // Format: "2.5 kW"
	Status      string `json:"status"`
	LastUpdated string `json:"last_updated"`
}

type APIResponse struct {
	Data  []RawDeviceData `json:"data"`
	Error string          `json:"error,omitempty"`
}

// Application data structures

type DeviceData struct {
	SerialNumber string    `json:"serialNumber"`
	Power        float64   `json:"power"`
	Status       string    `json:"status"`
	LastUpdated  time.Time `json:"lastUpdated"`
}

type AggregatedReport struct {
	TotalDevices      int          `json:"totalDevices"`
	SuccessfulDevices int          `json:"successfulDevices"`
	FailedDevices     int          `json:"failedDevices"`
	TotalPower        float64      `json:"totalPower"`
	AveragePower      float64      `json:"averagePower"`
	DeviceData        []DeviceData `json:"deviceData"`
}

// generateSerialNumbers creates dummy serials SN-000 to SN-499
func generateSerialNumbers() []string {
	serials := make([]string, totalDevices)
	for i := 0; i < totalDevices; i++ {
		serials[i] = fmt.Sprintf("SN-%03d", i)
	}
	return serials
}

// generateSignature computes MD5(path + token + timestamp)
func generateSignature(path, token, ts string) string {
	hash := md5.Sum([]byte(path + token + ts))
	return fmt.Sprintf("%x", hash)
}

// parsePower extracts the float value from strings like "2.5 kW"
func parsePower(p string) float64 {
	fields := strings.Fields(p)
	if len(fields) > 0 {
		if val, err := strconv.ParseFloat(fields[0], 64); err == nil {
			return val
		}
	}
	return 0.0
}

// fetchBatch queries the API for a chunk of devices with context support
func fetchBatch(ctx context.Context, client *http.Client, batch []string, token string) ([]DeviceData, error) {
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	sig := generateSignature(apiEndpoint, token, ts)

	payload, err := json.Marshal(map[string]interface{}{"sn_list": batch})
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiBaseURL+apiEndpoint, bytes.NewBuffer(payload))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Signature", sig)
	req.Header.Set("Timestamp", ts)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("rate limited (429)")
	}

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	devices := make([]DeviceData, 0, len(apiResp.Data))
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

// processQueue handles the concurrent fetching with strict rate limiting
func processQueue(ctx context.Context, serials []string) *AggregatedReport {
	token := getToken()

	type Job struct {
		ID    int
		Batch []string
	}
	type Result struct {
		Devices []DeviceData
		Error   error
		Batch   []string
	}

	numBatches := (len(serials) + batchSize - 1) / batchSize
	jobs := make(chan Job, numBatches)
	results := make(chan Result, numBatches)

	// Pre-fill job queue
	for i := 0; i < len(serials); i += batchSize {
		end := i + batchSize
		if end > len(serials) {
			end = len(serials)
		}
		jobs <- Job{
			ID:    (i / batchSize) + 1,
			Batch: serials[i:end],
		}
	}
	close(jobs)

	var wg sync.WaitGroup
	wg.Add(1)

	// Worker routine
	go func() {
		defer wg.Done()
		client := &http.Client{Timeout: 10 * time.Second}
		ticker := time.NewTicker(rateLimit)
		defer ticker.Stop()

		first := true

		for job := range jobs {
			// Check for context cancellation
			select {
			case <-ctx.Done():
				results <- Result{Error: ctx.Err(), Batch: job.Batch}
				continue
			default:
			}

			// Rate limit (skip first)
			if !first {
				<-ticker.C
			}
			first = false

			log.Printf("Batch %d/%d (%d devices)...", job.ID, numBatches, len(job.Batch))

			var devices []DeviceData
			var err error

			for i := 0; i < maxRetries; i++ {
				devices, err = fetchBatch(ctx, client, job.Batch, token)
				if err == nil {
					break
				}

				log.Printf("Batch %d retry %d/%d: %v", job.ID, i+1, maxRetries, err)
				time.Sleep(retryDelay * time.Duration(i+1))
			}

			results <- Result{Devices: devices, Error: err, Batch: job.Batch}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	report := &AggregatedReport{
		TotalDevices: len(serials),
		DeviceData:   make([]DeviceData, 0, len(serials)),
	}

	for res := range results {
		if res.Error != nil {
			log.Printf("Failed batch: %v", res.Error)
			report.FailedDevices += len(res.Batch)
		} else {
			report.SuccessfulDevices += len(res.Devices)
			report.DeviceData = append(report.DeviceData, res.Devices...)
		}
	}

	if report.SuccessfulDevices > 0 {
		var sumPower float64
		for _, d := range report.DeviceData {
			sumPower += d.Power
		}
		report.TotalPower = sumPower
		report.AveragePower = sumPower / float64(report.SuccessfulDevices)
	}

	return report
}

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Println("Starting EnergyGrid Aggregator...")

	if _, err := url.Parse(apiBaseURL); err != nil {
		log.Fatalf("Bad URL config: %v", err)
	}

	ctx := context.Background()

	start := time.Now()
	report := processQueue(ctx, generateSerialNumbers())
	elapsed := time.Since(start)

	log.Println("\n=== Final Report ===")
	log.Printf("Devices: %d Total | %d OK | %d Failed",
		report.TotalDevices, report.SuccessfulDevices, report.FailedDevices)
	log.Printf("Power:   %.2f kW Total", report.TotalPower)
	log.Printf("Time:    %s", elapsed)

	data, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile("aggregated_report.json", data, 0644); err != nil {
		log.Fatalf("Write failed: %v", err)
	}
	log.Println("Saved to aggregated_report.json")
}
