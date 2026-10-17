package main

import "os"

// toolBinary keeps existing manual installations working while preferring the
// app-private tools supplied by the portable installer.
func toolBinary(preferred, legacy string) string {
	// A broken/private non-executable tool is an installation error, not an
	// invitation to silently launch another service's shared executable.
	if _, err := os.Lstat(preferred); os.IsNotExist(err) {
		return legacy
	}
	return preferred
}

func mihomoBinary() string {
	return toolBinary("/usr/libexec/warpd/mihomo", mihomoBin)
}

func warpScoutBinary() string {
	return toolBinary("/usr/libexec/warpd/warpscout", warpScout)
}
