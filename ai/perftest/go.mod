module ai/perftest

go 1.27.1

require ai v0.0.0

require (
	dload v0.0.0-00010101000000-000000000000 // indirect
	github.com/ebitengine/purego v0.11.0 // indirect
	github.com/mavity/rusticated/platform v0.0.0-00010101000000-000000000000 // indirect
)

replace ai => ..

replace dload => ../../dload

replace github.com/mavity/rusticated/platform => ../../platform
