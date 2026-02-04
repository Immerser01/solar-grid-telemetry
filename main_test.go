package main

import (
	"testing"
)

func TestGenerateSignature(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		token    string
		ts       string
		expected string
	}{
		{
			name:  "standard signature",
			path:  "/device/real/query",
			token: "interview_token_123",
			ts:    "1234567890",
			// Calculated via: echo -n "/device/real/queryinterview_token_1231234567890" | md5sum
			expected: "33032594abbfabd4b36dae2c9dcdb11b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateSignature(tt.path, tt.token, tt.ts)
			if got != tt.expected {
				t.Errorf("generateSignature() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestGenerateSerialNumbers(t *testing.T) {
	serials := generateSerialNumbers()

	if len(serials) != totalDevices {
		t.Errorf("Expected %d devices, got %d", totalDevices, len(serials))
	}

	// Verify format of a few samples
	expectedFirst := "SN-000"
	if serials[0] != expectedFirst {
		t.Errorf("First serial invalid: got %s, want %s", serials[0], expectedFirst)
	}

	expectedLast := "SN-499"
	if serials[len(serials)-1] != expectedLast {
		t.Errorf("Last serial invalid: got %s, want %s", serials[len(serials)-1], expectedLast)
	}
}

func TestGenerateSignature_Deterministic(t *testing.T) {
	// Same inputs should always produce same output
	sig1 := generateSignature("/test", "token", "12345")
	sig2 := generateSignature("/test", "token", "12345")

	if sig1 != sig2 {
		t.Errorf("Signature should be deterministic: %s != %s", sig1, sig2)
	}
}

func TestGenerateSignature_DifferentInputs(t *testing.T) {
	sig1 := generateSignature("/path1", "token", "12345")
	sig2 := generateSignature("/path2", "token", "12345")

	if sig1 == sig2 {
		t.Error("Different paths should produce different signatures")
	}
}

// TestParsePower handles the tricky string parsing cases.
// The API returns power as strings like "2.5 kW", so we need to ensure we can
// strip the units and handle edge cases like missing values or bad formatting.
func TestParsePower(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected float64
	}{
		{"standard format", "2.5 kW", 2.5},
		{"integer value", "5 kW", 5.0},
		{"no unit", "3.14", 3.14},
		{"zero", "0 kW", 0.0},
		{"negative", "-1.5 kW", -1.5},
		{"empty string", "", 0.0},
		{"only unit", "kW", 0.0},
		{"whitespace only", "   ", 0.0},
		{"multiple spaces", "2.5   kW", 2.5},
		{"leading spaces", "  2.5 kW", 2.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePower(tt.input)
			if got != tt.expected {
				t.Errorf("parsePower(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParsePower_EdgeCases(t *testing.T) {
	// Very large number
	got := parsePower("999999.99 kW")
	if got != 999999.99 {
		t.Errorf("Failed on large number: got %v", got)
	}

	// Very small number
	got = parsePower("0.001 kW")
	if got != 0.001 {
		t.Errorf("Failed on small number: got %v", got)
	}
}
