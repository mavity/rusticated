//go:build !wasip1 && !darwin

package platform

func KeyboardLayoutID() string { return "" }
func SystemLocale() string     { return "" }
func SystemTimeZone() string   { return "" }
