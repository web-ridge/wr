//go:build !darwin

package main

import "context"

// watchNative is not available on non-macOS platforms
func watchNative(ctx context.Context, backendPath, frontendPath string, goOnly bool) {
	// Fall back to fsnotify-based watcher
	watch(ctx, backendPath, frontendPath, goOnly)
}

// useNativeWatcher returns false on non-macOS platforms
func useNativeWatcher() bool {
	return false
}
