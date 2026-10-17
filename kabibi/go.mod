module github.com/mavity/rusticated/kabibi

go 1.27.1

require (
	ai v0.0.0-00010101000000-000000000000
	github.com/alecthomas/chroma/v2 v2.27.0
	github.com/atotto/clipboard v0.1.4
	github.com/charmbracelet/ultraviolet v0.0.0-20260922123528-4e49372c11f9
	github.com/charmbracelet/x/ansi v0.11.8
	github.com/charmbracelet/x/input v0.3.7
	github.com/charmbracelet/x/term v0.2.2
	github.com/charmbracelet/x/vt v0.0.0-20260924144451-d676b019604b
	github.com/mattn/go-runewidth v0.0.30
	github.com/spf13/pflag v1.0.10
	mvdan.cc/sh/moreinterp v0.0.0-20260602220355-3aa9ec1fa7d0
	mvdan.cc/sh/v3 v3.14.1
	pty v0.0.0-00010101000000-000000000000
)

require (
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/x/exp/ordered v0.1.0 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/dlclark/regexp2/v2 v2.8.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/ebitengine/purego v0.11.1 // indirect
	github.com/klauspost/compress v1.20.1 // indirect
	github.com/klauspost/pgzip v1.2.6 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/pierrec/lz4/v4 v4.1.30 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/u-root/u-root v0.16.0 // indirect
	github.com/u-root/uio v0.0.0-20240224005618-d2acac8f3701 // indirect
	github.com/xo/terminfo v1.2.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/term v0.46.0 // indirect
)

replace (
	ai => ../ai
	github.com/atotto/clipboard => ../mohabbat/rusticated-jit/github.com/atotto/clipboard
	github.com/charmbracelet/x/term => ../mohabbat/rusticated-jit/github.com/charmbracelet/x/term
	github.com/mattn/go-isatty => ../mohabbat/rusticated-jit/github.com/mattn/go-isatty
	github.com/u-root/u-root => ../mohabbat/rusticated-jit/github.com/u-root/u-root
	golang.org/x/sys => ../mohabbat/rusticated-jit/golang.org/x/sys
	golang.org/x/term => ../mohabbat/rusticated-jit/golang.org/x/term
	pty => ../pty
)
