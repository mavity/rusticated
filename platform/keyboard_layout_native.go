//go:build !wasip1 && !darwin && !windows && !linux

package platform

func KeyboardLayoutID() string { return "" }
func SystemLocale() string     { return "" }
func SystemTimeZone() string   { return "" }
func SystemAppearance() string { return "" }
func PowerSource() string      { return "" }
