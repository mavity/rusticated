//go:build windows

package platform

import (
	"syscall"
	"unsafe"
)

var (
	modUser32   = syscall.NewLazyDLL("user32.dll")
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")
	modAdvapi32 = syscall.NewLazyDLL("advapi32.dll")

	procGetKeyboardLayoutNameW   = modUser32.NewProc("GetKeyboardLayoutNameW")
	procGetUserDefaultLocaleName = modKernel32.NewProc("GetUserDefaultLocaleName")
	procGetDynamicTimeZoneInfo   = modKernel32.NewProc("GetDynamicTimeZoneInformation")
	procGetSystemPowerStatus     = modKernel32.NewProc("GetSystemPowerStatus")
	procRegOpenKeyExW            = modAdvapi32.NewProc("RegOpenKeyExW")
	procRegQueryValueExW         = modAdvapi32.NewProc("RegQueryValueExW")
	procRegCloseKey              = modAdvapi32.NewProc("RegCloseKey")
)

type systemPowerStatus struct {
	ACLineStatus        uint8
	BatteryFlag         uint8
	BatteryLifePercent  uint8
	SystemStatusFlag    uint8
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

type dynamicTimeZoneInformation struct {
	Bias                        int32
	StandardName                [32]uint16
	StandardDate                [8]uint16
	StandardBias                int32
	DaylightName                [32]uint16
	DaylightDate                [8]uint16
	DaylightBias                int32
	TimeZoneKeyName             [128]uint16
	DynamicDaylightTimeDisabled uint8
	_                           [3]byte
}

func KeyboardLayoutID() string {
	var buf [9]uint16
	r1, _, _ := procGetKeyboardLayoutNameW.Call(uintptr(unsafe.Pointer(&buf[0])))
	if r1 == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:])
}

func SystemLocale() string {
	var buf [85]uint16
	r1, _, _ := procGetUserDefaultLocaleName.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if r1 == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:])
}

func SystemTimeZone() string {
	var info dynamicTimeZoneInformation
	r1, _, _ := procGetDynamicTimeZoneInfo.Call(uintptr(unsafe.Pointer(&info)))
	if uint32(r1) == 0xffffffff {
		return ""
	}
	return syscall.UTF16ToString(info.TimeZoneKeyName[:])
}

func SystemAppearance() string {
	const (
		hkeyCurrentUser = uintptr(0x80000001)
		keyRead         = uintptr(0x20019)
		regDword        = uint32(4)
	)
	subkey, err := syscall.UTF16PtrFromString(`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`)
	if err != nil {
		return ""
	}
	valueName, err := syscall.UTF16PtrFromString("AppsUseLightTheme")
	if err != nil {
		return ""
	}
	var hKey uintptr
	r1, _, _ := procRegOpenKeyExW.Call(
		hkeyCurrentUser,
		uintptr(unsafe.Pointer(subkey)),
		0,
		keyRead,
		uintptr(unsafe.Pointer(&hKey)),
	)
	if r1 != 0 {
		return ""
	}
	defer procRegCloseKey.Call(hKey)

	var valType uint32
	var data uint32
	dataSize := uint32(unsafe.Sizeof(data))
	r1, _, _ = procRegQueryValueExW.Call(
		hKey,
		uintptr(unsafe.Pointer(valueName)),
		0,
		uintptr(unsafe.Pointer(&valType)),
		uintptr(unsafe.Pointer(&data)),
		uintptr(unsafe.Pointer(&dataSize)),
	)
	if r1 != 0 || valType != regDword || dataSize != 4 {
		return ""
	}
	if data == 0 {
		return "dark"
	}
	return "light"
}

func PowerSource() string {
	var status systemPowerStatus
	r1, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&status)))
	if r1 == 0 {
		return ""
	}
	switch status.ACLineStatus {
	case 1:
		return "ac"
	case 0:
		return "battery"
	default:
		return ""
	}
}
