module github.com/mavity/rusticated/kabibi

go 1.27.1

require (
	github.com/alecthomas/chroma/v2 v2.27.0
	github.com/atotto/clipboard v0.1.4
	github.com/charmbracelet/bubbles v1.0.0
	github.com/charmbracelet/bubbletea v1.3.10
	github.com/charmbracelet/glamour v1.0.0
	github.com/charmbracelet/lipgloss v1.1.1-0.20250404203927-76690c660834
	github.com/charmbracelet/ultraviolet v0.0.0-20260910203606-6c9e17dc7a16
	github.com/charmbracelet/x/ansi v0.11.8
	github.com/charmbracelet/x/vt v0.0.0-20260913004009-c615ff2f7805
	github.com/ebitengine/purego v0.11.0
	github.com/mattn/go-runewidth v0.0.30
	github.com/vito/midterm v0.2.5
	mvdan.cc/sh/moreinterp v0.0.0-20260602220355-3aa9ec1fa7d0
	mvdan.cc/sh/v3 v3.14.1
)

require (
	github.com/PuerkitoBio/goquery v1.13.0 // indirect
	github.com/andybalholm/cascadia v1.3.4 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.15 // indirect
	github.com/charmbracelet/x/exp/ordered v0.1.0 // indirect
	github.com/charmbracelet/x/exp/slice v0.0.0-20260913004009-c615ff2f7805 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/danielgatis/go-ansicode v1.0.14 // indirect
	github.com/danielgatis/go-iterator v0.0.1 // indirect
	github.com/danielgatis/go-utf8 v1.0.1 // indirect
	github.com/danielgatis/go-vte v1.0.11 // indirect
	github.com/dlclark/regexp2/v2 v2.8.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/erikgeiser/coninput v0.0.0-20211004153227-1c3628e74d0f // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/hugelgupf/go-shlex v0.0.0-20200702092117-c80c9d0918fa // indirect
	github.com/hugelgupf/vmtest v0.0.0-20240307030256-5d9f3d34a58d // indirect
	github.com/klauspost/compress v1.20.0 // indirect
	github.com/klauspost/pgzip v1.2.6 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mattn/go-localereader v0.0.1 // indirect
	github.com/microcosm-cc/bluemonday v1.0.27 // indirect
	github.com/muesli/ansi v0.0.0-20230316100256-276c6243b2f6 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/muesli/reflow v0.3.0 // indirect
	github.com/muesli/termenv v0.16.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.30 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/u-root/gobusybox/src v0.0.0-20250101170133-2e884e4509c7 // indirect
	github.com/u-root/mkuimage v0.0.0-20250905073043-9a40452f5d3b // indirect
	github.com/u-root/u-root v1.0.1 // indirect
	github.com/u-root/uio v0.0.0-20240224005618-d2acac8f3701 // indirect
	github.com/xo/terminfo v1.2.0 // indirect
	github.com/yuin/goldmark v1.8.6 // indirect
	github.com/yuin/goldmark-emoji v1.0.6 // indirect
	golang.org/x/exp v0.0.0-20250305212735-054e65f0b394 // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/term v0.46.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	golang.org/x/tools v0.49.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/atotto/clipboard => ../mohabbat/rusticated-jit/github.com/atotto/clipboard
	github.com/charmbracelet/bubbletea => ../mohabbat/rusticated-jit/github.com/charmbracelet/bubbletea
	github.com/charmbracelet/x/term => ../mohabbat/rusticated-jit/github.com/charmbracelet/x/term
	github.com/mattn/go-isatty => ../mohabbat/rusticated-jit/github.com/mattn/go-isatty
	github.com/muesli/termenv => ../mohabbat/rusticated-jit/github.com/muesli/termenv
	github.com/u-root/u-root => ../mohabbat/rusticated-jit/github.com/u-root/u-root
	golang.org/x/sys => ../mohabbat/rusticated-jit/golang.org/x/sys
	golang.org/x/term => ../mohabbat/rusticated-jit/golang.org/x/term
)
