#include <stdlib.h>
#include <string.h>
#include <CoreFoundation/CoreFoundation.h>
#include <IOKit/pwr_mgt/IOPMLib.h>
#include "iokit.h"

static CFDateRef date_from_unix(double unix_seconds) {
	return CFDateCreate(kCFAllocatorDefault, unix_seconds - kCFAbsoluteTimeIntervalSince1970);
}

static CFStringRef cfstr(const char *s) {
	return CFStringCreateWithCString(kCFAllocatorDefault, s, kCFStringEncodingUTF8);
}

static void append_cstring(CFMutableStringRef out, CFStringRef s) {
	if (s && CFGetTypeID(s) == CFStringGetTypeID()) {
		CFStringAppend(out, s);
	}
}

// schlaflos_list_events renders every scheduled power event as one
// "unix_seconds\towner\ttype\n" line. The caller frees the result.
char *schlaflos_list_events(void) {
	CFMutableStringRef out = CFStringCreateMutable(kCFAllocatorDefault, 0);
	CFArrayRef events = IOPMCopyScheduledPowerEvents();
	if (events) {
		CFIndex n = CFArrayGetCount(events);
		for (CFIndex i = 0; i < n; i++) {
			CFDictionaryRef ev = CFArrayGetValueAtIndex(events, i);
			if (!ev || CFGetTypeID(ev) != CFDictionaryGetTypeID()) {
				continue;
			}
			CFDateRef when = CFDictionaryGetValue(ev, CFSTR(kIOPMPowerEventTimeKey));
			if (!when || CFGetTypeID(when) != CFDateGetTypeID()) {
				continue;
			}
			double unix_seconds = CFDateGetAbsoluteTime(when) + kCFAbsoluteTimeIntervalSince1970;
			CFStringAppendFormat(out, NULL, CFSTR("%.0f\t"), unix_seconds);
			append_cstring(out, CFDictionaryGetValue(ev, CFSTR(kIOPMPowerEventAppNameKey)));
			CFStringAppend(out, CFSTR("\t"));
			append_cstring(out, CFDictionaryGetValue(ev, CFSTR(kIOPMPowerEventTypeKey)));
			CFStringAppend(out, CFSTR("\n"));
		}
		CFRelease(events);
	}
	CFIndex max = CFStringGetMaximumSizeForEncoding(CFStringGetLength(out), kCFStringEncodingUTF8) + 1;
	char *buf = malloc((size_t)max);
	if (buf && !CFStringGetCString(out, buf, max, kCFStringEncodingUTF8)) {
		buf[0] = '\0';
	}
	CFRelease(out);
	return buf;
}

int schlaflos_schedule_event(double unix_seconds, const char *owner, const char *type) {
	CFDateRef when = date_from_unix(unix_seconds);
	CFStringRef id = cfstr(owner);
	CFStringRef t = cfstr(type);
	IOReturn r = IOPMSchedulePowerEvent(when, id, t);
	CFRelease(when);
	CFRelease(id);
	CFRelease(t);
	return (int)r;
}

int schlaflos_cancel_event(double unix_seconds, const char *owner, const char *type) {
	CFDateRef when = date_from_unix(unix_seconds);
	CFStringRef id = cfstr(owner);
	CFStringRef t = cfstr(type);
	IOReturn r = IOPMCancelScheduledPowerEvent(when, id, t);
	CFRelease(when);
	CFRelease(id);
	CFRelease(t);
	return (int)r;
}
