//go:build darwin && cgo

package platform

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Carbon
#import <Carbon/Carbon.h>

static int rusticated_keyboard_layout_id(char *buffer, CFIndex capacity) {
	TISInputSourceRef source = TISCopyCurrentKeyboardInputSource();
	if (source == NULL) {
		return 0;
	}
	CFStringRef identifier = (CFStringRef) TISGetInputSourceProperty(source, kTISPropertyInputSourceID);
	int copied = identifier != NULL &&
		CFStringGetCString(identifier, buffer, capacity, kCFStringEncodingUTF8);
	CFRelease(source);
	return copied;
}

static int rusticated_system_locale(char *buffer, CFIndex capacity) {
	CFLocaleRef locale = CFLocaleCopyCurrent();
	if (locale == NULL) {
		return 0;
	}
	CFStringRef identifier = CFLocaleGetIdentifier(locale);
	int copied = identifier != NULL &&
		CFStringGetCString(identifier, buffer, capacity, kCFStringEncodingUTF8);
	CFRelease(locale);
	return copied;
}

static int rusticated_system_time_zone(char *buffer, CFIndex capacity) {
	CFTimeZoneRef timeZone = CFTimeZoneCopySystem();
	if (timeZone == NULL) {
		return 0;
	}
	CFStringRef name = CFTimeZoneGetName(timeZone);
	int copied = name != NULL &&
		CFStringGetCString(name, buffer, capacity, kCFStringEncodingUTF8);
	CFRelease(timeZone);
	return copied;
}
*/
import "C"

func copySystemString(get func(*C.char, C.CFIndex) C.int) string {
	var buffer [256]C.char
	if get(&buffer[0], C.CFIndex(len(buffer))) == 0 {
		return ""
	}
	return C.GoString(&buffer[0])
}

func KeyboardLayoutID() string {
	return copySystemString(func(buffer *C.char, capacity C.CFIndex) C.int {
		return C.rusticated_keyboard_layout_id(buffer, capacity)
	})
}

func SystemLocale() string {
	return copySystemString(func(buffer *C.char, capacity C.CFIndex) C.int {
		return C.rusticated_system_locale(buffer, capacity)
	})
}

func SystemTimeZone() string {
	return copySystemString(func(buffer *C.char, capacity C.CFIndex) C.int {
		return C.rusticated_system_time_zone(buffer, capacity)
	})
}
