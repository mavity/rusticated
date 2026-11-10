module washmhost

go 1.27.1

require (
	github.com/mavity/rusticated/platform v0.0.0-00010101000000-000000000000
	github.com/tetratelabs/wazero v1.12.0
	golang.org/x/term v0.29.0
)

require (
	github.com/ebitengine/purego v0.11.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
)

replace github.com/mavity/rusticated/platform => ../../platform
