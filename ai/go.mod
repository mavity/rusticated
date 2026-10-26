module ai

go 1.27.1

require (
	dload v0.0.0-00010101000000-000000000000
	github.com/ebitengine/purego v0.11.0
	github.com/mavity/rusticated/platform v0.0.0-00010101000000-000000000000
)

replace (
	dload => ../dload
	github.com/mavity/rusticated/platform => ../platform
)
