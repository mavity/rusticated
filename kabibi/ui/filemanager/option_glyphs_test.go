package filemanager

import (
	"testing"

	"github.com/charmbracelet/x/input"
)

func TestNormalizeMacOptionGlyph(t *testing.T) {
	tests := []struct {
		name     string
		event    input.KeyPressEvent
		layoutID string
		want     input.KeyPressEvent
	}{
		{
			name:     "ABC layout glyph",
			event:    input.KeyPressEvent{Code: '†', Text: "†"},
			layoutID: "com.apple.keylayout.ABC",
			want:     input.KeyPressEvent{Code: 't', BaseCode: 't', Text: "t", Mod: input.ModAlt},
		},
		{
			name:     "US layout digit glyph",
			event:    input.KeyPressEvent{Code: '¶', Text: "¶"},
			layoutID: "com.apple.keylayout.US",
			want:     input.KeyPressEvent{Code: '7', BaseCode: '7', Text: "7", Mod: input.ModAlt},
		},
		{
			name:     "unrecognized layout remains unchanged",
			event:    input.KeyPressEvent{Code: '†', Text: "†"},
			layoutID: "com.apple.keylayout.French",
			want:     input.KeyPressEvent{Code: '†', Text: "†"},
		},
		{
			name:     "modified event remains unchanged",
			event:    input.KeyPressEvent{Code: '†', Text: "†", Mod: input.ModShift},
			layoutID: "com.apple.keylayout.ABC",
			want:     input.KeyPressEvent{Code: '†', Text: "†", Mod: input.ModShift},
		},
		{
			name:     "multi-rune text remains unchanged",
			event:    input.KeyPressEvent{Code: '†', Text: "††"},
			layoutID: "com.apple.keylayout.ABC",
			want:     input.KeyPressEvent{Code: '†', Text: "††"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeMacOptionGlyph(tt.event, tt.layoutID)
			if got.Code != tt.want.Code || got.Text != tt.want.Text || got.Mod != tt.want.Mod {
				t.Fatalf("normalized event = {Code:%q Text:%q Mod:%v}, want {Code:%q Text:%q Mod:%v}",
					got.Code, got.Text, got.Mod, tt.want.Code, tt.want.Text, tt.want.Mod)
			}
			if tt.want.Mod == input.ModAlt && (got.BaseCode != tt.want.Code || got.ShiftedCode != 0) {
				t.Fatalf("normalized codes = {BaseCode:%q ShiftedCode:%q}, want base %q and no shifted code",
					got.BaseCode, got.ShiftedCode, tt.want.Code)
			}
		})
	}
}
