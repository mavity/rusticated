package main

import (
	"reflect"
	"testing"
)

func TestDecodeWashmhostGuestArgs(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{name: "empty", want: nil},
		{
			name: "preserves dotted flags and spaces",
			raw:  `["-test.v","-test.timeout=90s","value with spaces"]`,
			want: []string{"-test.v", "-test.timeout=90s", "value with spaces"},
		},
		{name: "rejects malformed JSON", raw: `["-test.v"`, wantErr: true},
		{name: "rejects non-array JSON", raw: `null`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeWashmhostGuestArgs(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("decodeWashmhostGuestArgs(%q) error = %v, wantErr %t", tt.raw, err, tt.wantErr)
			}
			if err == nil && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("decodeWashmhostGuestArgs(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
