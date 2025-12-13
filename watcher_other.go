//go:build !darwin

package main

// watchNative is not available on non-macOS platforms
func watchNative(backendPath, frontendPath string) {
	// Fall back to fsnotify-based watcher
	watch(backendPath, frontendPath)
}

// useNativeWatcher returns false on non-macOS platforms
func useNativeWatcher() bool {
	return false
}
