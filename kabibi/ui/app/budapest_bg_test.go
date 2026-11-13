package app

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/input"
	xterm "github.com/charmbracelet/x/term"
	"github.com/charmbracelet/x/vt"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

func TestBudapestScene_RendersAndEvolvesOverTime(t *testing.T) {
	scene := newBudapestScene()
	frame0 := append([]uint8(nil), scene.renderAt(0.0)...)
	frame1 := append([]uint8(nil), scene.renderAt(1.5)...)

	if len(frame0) != budapestW*budapestH*4 {
		t.Fatalf("rendered frame length = %d, want %d", len(frame0), budapestW*budapestH*4)
	}
	if bytes.Equal(frame0, frame1) {
		t.Fatal("expected animated Budapest frame to evolve between t=0.0s and t=1.5s")
	}
}

func TestCenteredCropBounds_CropsTopAndBottomForStandardTerminal(t *testing.T) {
	cropX, cropY, cropW, cropH := centeredCropBounds(80, 24)
	if cropX != 0 || cropW != float64(budapestW) {
		t.Fatalf("cropX=%v cropW=%v, want full width (0, %d)", cropX, cropW, budapestW)
	}
	if cropH <= 0 || cropH >= float64(budapestH) {
		t.Fatalf("cropH=%v, want strictly between 0 and %d", cropH, budapestH)
	}
	wantY := (float64(budapestH) - cropH) * 0.5
	if cropY != wantY {
		t.Fatalf("cropY=%v, want centered %v", cropY, wantY)
	}
}

func TestBudapestBackground_ExemptsBluePanelsDialogsAndScrollback(t *testing.T) {
	if !EnableBudapestBackground {
		t.Skip("EnableBudapestBackground is false")
	}

	var scrollback strings.Builder
	widget := NewWidget(AppWidgetOptions{
		WorkingDir:        t.TempDir(),
		PanelsVisible:     true,
		FSService:         deterministicSnapshotFS{},
		WriteToScrollback: func(s string) { scrollback.WriteString(s) },
	})
	defer widget.Close()

	rect := ui.Rect{W: 80, H: 24}
	theme := ui.Theme{
		DefaultForeground: 0xE5E5E5,
		DefaultBackground: 0x000000,
		HasDefaultFG:      true,
		HasDefaultBG:      true,
	}

	// 1. With blue panels visible: panel cells must remain pure blue (0x0000A8),
	// while shell gap cells below the panels are shifted slightly from 0x000000.
	buf, _ := renderAppANSIForTest(t, widget.Render(rect, ui.RenderContext{
		Focused: true,
		Theme:   theme,
	}), rect.W, rect.H)

	panelCell := buf.Get(10, 8)
	if got := panelCell.Style.Background(); got != terminal.Color(0x0000A8) {
		t.Fatalf("blue panel cell background = %#06x, want untouched %#06x", got, 0x0000A8)
	}

	shellGapCell := buf.Get(40, 20)
	if shellGapCell.Style.HasAttribute(terminal.CharResetBackgroundDefault) {
		t.Fatal("shell gap cell should have explicit Budapest-shifted RGB background, not default reset")
	}
	if got := shellGapCell.Style.Background(); got == 0x000000 {
		t.Fatal("expected shell gap cell background to be shifted slightly above 0x000000")
	}

	// 2. Open a modal dialog while panels are visible:
	// - Dialog interior cells remain untouched (colorDlgBg = 0x2D3642).
	// - Dialog shadow over blue panel darkens pure blue (0x0000A8) without Budapest tint.
	widget.openMkdirDialog()
	dlgSize := widget.Dialog().Measure(ui.Constraints{MaxW: rect.W, MaxH: rect.H})
	bufDlg, _ := renderAppANSIForTest(t, widget.Render(rect, ui.RenderContext{
		Focused: true,
		Theme:   theme,
	}), rect.W, rect.H)

	dx := (bufDlg.Width - dlgSize.W) / 2
	dy := (bufDlg.Height - dlgSize.H) / 2
	if bufDlg.Height >= dlgSize.H+2 && dy+dlgSize.H+2 > bufDlg.Height {
		dy = bufDlg.Height - dlgSize.H - 2
	}
	dlgInterior := bufDlg.Get(dx+2, dy+1)
	if got := dlgInterior.Style.Background(); got != terminal.Color(0x2D3642) {
		t.Fatalf("dialog interior background = %#06x, want untouched %#06x", got, 0x2D3642)
	}

	// Shadow cell falling over the blue panel must only have blue channel (R==0, G==0, B>0),
	// proving no Budapest warm tint bled into the blue panel under the shadow.
	shadowOnPanel := bufDlg.Get(dx+dlgSize.W, dy+2).Style.Background()
	if (shadowOnPanel>>16)&0xFF != 0 || (shadowOnPanel>>8)&0xFF != 0 || (shadowOnPanel&0xFF) == 0 || shadowOnPanel >= 0x0000A8 {
		t.Fatalf("dialog shadow over blue panel = %#06x, want darkened pure blue (< 0x0000A8 with R=0, G=0)", shadowOnPanel)
	}

	// 3. Eject lines into scrollback in stack mode: ejected scrollback must not contain
	// Budapest 24-bit background escapes (\x1b[48;2;).
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEscape})
	widget.SetPanelsVisible(false)
	smallRect := ui.Rect{W: 80, H: 6}
	for i := 0; i < 6; i++ {
		typeString(widget, "echo line")
		widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
		widget.Render(smallRect, ui.RenderContext{Focused: true, Theme: theme})
	}
	if strings.Contains(scrollback.String(), "\x1b[48;2;") {
		t.Fatalf("ejected scrollback contains 24-bit background colour escape: %q", scrollback.String())
	}
}

func TestBudapestTicker_DoesNotEjectIntoScrollbackAcrossFrames(t *testing.T) {
	if !EnableBudapestBackground {
		t.Skip("EnableBudapestBackground is false")
	}
	var scrollback strings.Builder
	var host *ui.Host
	widget := NewWidget(AppWidgetOptions{
		WorkingDir:        t.TempDir(),
		DisableAI:         false,
		WriteToScrollback: func(s string) { scrollback.WriteString(s) },
		OnExit: func() {
			if host != nil {
				host.Stop()
			}
		},
	})
	defer widget.Close()

	// Run through Host.RunWithIO for 250ms and inspect rawTermOut in vt.Emulator BEFORE Stop
	inR, inW := io.Pipe()
	defer inR.Close()
	defer inW.Close()
	var rawTermOut syncBuffer
	emu := vt.NewEmulator(80, 24)
	go func() {
		_, _ = io.Copy(inW, emu)
	}()
	for i := 0; i < 23; i++ {
		_, _ = emu.WriteString("prior terminal line\r\n")
	}
	t.Logf("BEFORE Kabibi: scrollback=%d, cursor=(%d,%d)", emu.Scrollback().Len(), emu.CursorPosition().X, emu.CursorPosition().Y)

	host = ui.NewHost(widget, true)
	runDone := make(chan error, 1)
	go func() {
		runDone <- host.RunWithIO(
			inR,
			io.MultiWriter(&rawTermOut, emu),
			func(uintptr) (*xterm.State, error) { return &xterm.State{}, nil },
			func(uintptr, *xterm.State) error { return nil },
			func(uintptr) (int, int, error) { return 80, 24, nil },
		)
	}()
	time.Sleep(1200 * time.Millisecond)
	t.Logf("AFTER 1200ms (before Stop): scrollback=%d, cursor=(%d,%d)",
		emu.Scrollback().Len(), emu.CursorPosition().X, emu.CursorPosition().Y)
	host.Stop()
	<-runDone
	for y := 0; y < 6; y++ {
		var rowStr strings.Builder
		for x := 0; x < 80; x++ {
			c := emu.CellAt(x, y)
			if c != nil && c.Width == 0 {
				continue
			}
			if c != nil && c.Content != "" {
				rowStr.WriteString(c.Content)
			} else {
				rowStr.WriteByte(' ')
			}
		}
		t.Logf("  row[%d]: %q", y, strings.TrimRight(rowStr.String(), " "))
	}
	for i := 0; i < emu.Scrollback().Len(); i++ {
		line := emu.Scrollback().Lines()[i]
		var rowStr strings.Builder
		hasBg := false
		for x := 0; x < len(line); x++ {
			if line[x].Style.Bg != nil {
				hasBg = true
			}
			if line[x].Width == 0 {
				continue
			}
			if line[x].Content != "" {
				rowStr.WriteString(line[x].Content)
			} else {
				rowStr.WriteByte(' ')
			}
		}
		t.Logf("  scrollback[%d] (hasBg=%v): %q", i, hasBg, strings.TrimRight(rowStr.String(), " "))
	}
	if scrollback.Len() > 0 {
		t.Fatalf("unexpected scrollback ejection during idle animation frames: %q", scrollback.String())
	}
}
