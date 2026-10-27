module github.com/mavity/rusticated/pope

go 1.27.1

require (
	ai    v0.0.0-00010101000000-000000000000
	dload v0.0.0-00010101000000-000000000000
	github.com/mavity/rusticated/platform v0.0.0-00010101000000-000000000000
)

require github.com/ebitengine/purego v0.11.0 // indirect

replace (
	ai    => ../ai
	dload => ../dload
	github.com/mavity/rusticated/platform => ../platform
)
