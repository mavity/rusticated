//go:build wasip1

package platform

import "syscall"

func KeyboardLayoutID() string {
	if info := syscall.GetPlatformInfo(); info != nil {
		return info.KeyboardLayoutID
	}
	return ""
}

func SystemLocale() string {
	if info := syscall.GetPlatformInfo(); info != nil {
		return info.Locale
	}
	return ""
}

func SystemTimeZone() string {
	if info := syscall.GetPlatformInfo(); info != nil {
		return info.TimeZone
	}
	return ""
}
