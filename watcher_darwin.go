//go:build darwin

package main

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/rjeczalik/notify"
	"github.com/rs/zerolog/log"
)

func watchNative(ctx context.Context, backendPath, frontendPath string, goOnly bool) {
	// Create a channel to receive file system events
	// Buffer size of 100 to avoid blocking
	c := make(chan notify.EventInfo, 100)

	// Get absolute path for the current directory
	absPath, err := filepath.Abs(".")
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get absolute path")
	}

	// Watch the current directory recursively using FSEvents on macOS
	// The "..." suffix means recursive watching
	if err := notify.Watch(absPath+"/...", c, notify.All); err != nil {
		log.Fatal().Err(err).Msg("cannot start native file watcher")
	}
	defer notify.Stop(c)

	// Also watch frontend paths
	frontendCustomSchema := filepath.Join(frontendPath, "schema_custom.graphql")
	frontendGenerated := filepath.Join(frontendPath, "src/__generated__")

	if err := notify.Watch(frontendCustomSchema, c, notify.All); err != nil {
		log.Error().Err(err).Str("path", frontendCustomSchema).Msg("failed to watch frontend schema")
	}
	if err := notify.Watch(frontendGenerated+"/...", c, notify.All); err != nil {
		log.Error().Err(err).Str("path", frontendGenerated).Msg("failed to watch frontend generated")
	}

	if goOnly {
		log.Info().Msg("native macOS file watcher started (FSEvents) - Go files only mode")
	} else {
		log.Info().Msg("native macOS file watcher started (FSEvents)")
	}

	// Process events with context cancellation support
	for {
		select {
		case <-ctx.Done():
			log.Debug().Msg("native file watcher stopping...")
			return
		case ei, ok := <-c:
			if !ok {
				return
			}
			handleNativeEvent(ei, absPath, goOnly)
		}
	}
}

func handleNativeEvent(ei notify.EventInfo, absPath string, goOnly bool) {
	path := ei.Path()

	// Convert to relative path for consistency
	relPath, err := filepath.Rel(absPath, path)
	if err != nil {
		relPath = path
	}

	// Skip excluded directories
	if strings.Contains(relPath, "models/") || strings.Contains(relPath, ".idea") {
		return
	}

	// Process write, create, and rename events (rename is used by atomic writes)
	event := ei.Event()
	if event&notify.Write == 0 && event&notify.Create == 0 && event&notify.Rename == 0 {
		return
	}

	log.Debug().Str("file", relPath).Str("event", event.String()).Msg("modified file (FSEvents)")

	// Check for generated files
	isGeneratedGo := strings.Contains(relPath, "generated_") &&
		(strings.Contains(relPath, ".go") || strings.Contains(relPath, ".gohtml"))
	if isGeneratedGo || strings.Contains(relPath, "__generated__/") {
		log.Debug().Msg("generated files changed, skipping")
		return
	}

	// In goOnly mode, only restart server on Go file changes
	if goOnly {
		if strings.Contains(relPath, ".go") || strings.Contains(relPath, ".gohtml") || strings.Contains(relPath, ".env") {
			log.Debug().Msg("restart server (go-only mode)")
			debounced(runGoChanged)
		}
		return
	}

	// Handle file changes based on type
	switch {
	case strings.Contains(relPath, ".sql"):
		log.Debug().Msg("sql changed, run migrations + convert plugin")
		debounced(runSqlChanged)
	case strings.Contains(relPath, ".graphql"):
		log.Debug().Msg("run convert & merge schemas with relay")
		debounced(runSchemaChanged)
	case strings.Contains(relPath, "seed/"):
		log.Debug().Msg("re-run seed.go")
		debounced(runSeedChanged)
	case strings.Contains(relPath, ".env") ||
		strings.Contains(relPath, ".go") ||
		strings.Contains(relPath, ".gohtml"):
		log.Debug().Msg("restart server")
		debounced(runGoChanged)
	case strings.Contains(relPath, "migrations/"):
		log.Debug().Msg("run migrations + convert plugin")
		debounced(runMigrationsChanged)
	}
}

// useNativeWatcher returns true on macOS to use FSEvents
func useNativeWatcher() bool {
	return true
}
