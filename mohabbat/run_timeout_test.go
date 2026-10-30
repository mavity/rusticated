package mohabbat

import (
	"testing"
	"time"
)

func TestParseGoTestTimeout(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    time.Duration
		wantErr bool
	}{
		{name: "not provided"},
		{name: "equals form", args: []string{"-test.timeout=90s"}, want: 90 * time.Second},
		{name: "separate value", args: []string{"-test.timeout", "2m"}, want: 2 * time.Minute},
		{name: "double dash", args: []string{"--test.timeout=3s"}, want: 3 * time.Second},
		{name: "last value wins", args: []string{"-test.timeout=2s", "-test.timeout", "4s"}, want: 4 * time.Second},
		{name: "zero disables timeout", args: []string{"-test.timeout=0"}},
		{name: "invalid duration", args: []string{"-test.timeout=soon"}, wantErr: true},
		{name: "missing value", args: []string{"-test.timeout"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseGoTestTimeout(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseGoTestTimeout(%q) error = %v, wantErr %t", tt.args, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("parseGoTestTimeout(%q) = %s, want %s", tt.args, got, tt.want)
			}
		})
	}
}
