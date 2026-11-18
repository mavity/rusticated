package main

import (
	"strings"
	"testing"
)

func TestValidateNativeAbiOutput(t *testing.T) {
	t.Run("exact lifecycle output", func(t *testing.T) {
		output := strings.Join([]string{
			"PASS lifecycle/tty",
		}, "\n")
		if err := validateNativeAbiOutput("lifecycle_tty", []byte(output)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("missing case", func(t *testing.T) {
		err := validateNativeAbiOutput("lifecycle_std_write", nil)
		if err == nil || !strings.Contains(err.Error(), "missing=") {
			t.Fatalf("expected missing-case error, got %v", err)
		}
	})

	t.Run("unexpected case", func(t *testing.T) {
		output := "PASS lifecycle/tty\nPASS lifecycle/not_a_real_case"
		err := validateNativeAbiOutput("lifecycle_tty", []byte(output))
		if err == nil || !strings.Contains(err.Error(), "unexpected=") {
			t.Fatalf("expected unexpected-case error, got %v", err)
		}
	})

	t.Run("duplicate case", func(t *testing.T) {
		output := strings.Join([]string{
			"PASS lifecycle/tty",
			"PASS lifecycle/tty",
		}, "\n")
		err := validateNativeAbiOutput("lifecycle_tty", []byte(output))
		if err == nil || !strings.Contains(err.Error(), "repeated 2 times") {
			t.Fatalf("expected duplicate-case error, got %v", err)
		}
	})

	t.Run("failure line", func(t *testing.T) {
		output := "FAIL lifecycle/tty code=8"
		err := validateNativeAbiOutput("lifecycle_tty", []byte(output))
		if err == nil || !strings.Contains(err.Error(), "suite reported failures") {
			t.Fatalf("expected failure-line error, got %v", err)
		}
	})

	t.Run("unknown suite", func(t *testing.T) {
		err := validateNativeAbiOutput("unknown", nil)
		if err == nil || !strings.Contains(err.Error(), "no expected PASS cases") {
			t.Fatalf("expected unknown-suite error, got %v", err)
		}
	})
}
