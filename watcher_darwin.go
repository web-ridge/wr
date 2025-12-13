//go:build darwin

package main

import (
	"path/filepath"
	"strings"

	"github.com/rjeczalik/notify"
	"github.com/rs/zerolog/log"
)

func watchNative(backendPath, frontendPath string) {
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

	log.Info().Msg("native macOS file watcher started (FSEvents via notify)")

	// Process events
	for ei := range c {
		path := ei.Path()

		// Convert to relative path for consistency
		relPath, err := filepath.Rel(absPath, path)
		if err != nil {
			relPath = path
		}

		// Skip excluded directories
		if strings.Contains(relPath, "models/") || strings.Contains(relPath, ".idea") {
			continue
		}

		// Only process write events
		event := ei.Event()
		if event&notify.Write == 0 && event&notify.Create == 0 {
			continue
		}

		log.Debug().Str("file", relPath).Str("event", event.String()).Msg("modified file (FSEvents)")

		// Check for generated files
		isGeneratedGo := strings.Contains(relPath, "generated_") &&
			(strings.Contains(relPath, ".go") || strings.Contains(relPath, ".gohtml"))
		if isGeneratedGo || strings.Contains(relPath, "__generated__/") {
			log.Debug().Msg("generated files changed, skipping")
			continue
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
}

// useNativeWatcher returns true on macOS to use FSEvents
func useNativeWatcher() bool {
	return true
}
