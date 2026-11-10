//go:build darwin && !cgo

package platform

func KeyboardLayoutID() string { return "" }
func SystemLocale() string     { return "" }
func SystemTimeZone() string   { return "" }
