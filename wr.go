package main

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/bep/debounce"
	"github.com/fsnotify/fsnotify"
	"github.com/gen2brain/beeep"
	_ "github.com/joho/godotenv/autoload"
	"github.com/rs/zerolog/log"
	"github.com/urfave/cli/v2"
	"github.com/web-ridge/wr/helpers"
	"github.com/web-ridge/wr/specific"
)

var (
	quit    = make(chan bool)
	restart = make(chan bool)
	port    = os.Getenv("PORT")
	db      *sql.DB
)

type paths struct {
	backend  string
	frontend string
	orgName  string
}

// main, setupPaths, installDependencies, installBun, installFrontendDependencies,
// installPrettier, installSqlBoiler, installSqlBoilerMysqlDriver, notify,
// stopServer, stopDocker, killPortProcess, runInitialSetup, runMigrations,
// dropDatabase, runConvertPlugin, runMergeSchemasWithRelay, runMergeSchemas,
// runRelay, runSeeder, getDirectoryWithSubDirectories unchanged

func startServerInBackground(restart bool) *exec.Cmd {
	killPortProcess(port)

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/C", fmt.Sprintf("set WR_RESTART=%v && go run server.go", restart))
	} else {
		cmd = exec.Command("/bin/sh", "-c", fmt.Sprintf("WR_RESTART=%v go run server.go", restart))
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Start server and monitor its health
	go func() {
		for attempt := 1; attempt <= 3; attempt++ {
			if err := cmd.Run(); err != nil && !strings.Contains(err.Error(), "signal: killed") {
				log.Error().Err(err).Int("attempt", attempt).Msg("failed to run server")
				notify("Server Error", fmt.Sprintf("Failed to run server (attempt %d): %v", attempt, err))
				if attempt < 3 {
					log.Info().Int("attempt", attempt).Msg("retrying server start")
					time.Sleep(time.Second * time.Duration(attempt))
					killPortProcess(port)
					// Recreate command for retry
					if runtime.GOOS == "windows" {
						cmd = exec.Command("cmd.exe", "/C", fmt.Sprintf("set WR_RESTART=%v && go run server.go", restart))
					} else {
						cmd = exec.Command("/bin/sh", "-c", fmt.Sprintf("WR_RESTART=%v go run server.go", restart))
					}
					cmd.Stdout = os.Stdout
					cmd.Stderr = os.Stderr
					cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
					continue
				}
			}
			break
		}
	}()

	// Wait to verify server startup
	time.Sleep(500 * time.Millisecond)
	if cmd.Process == nil || cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		log.Error().Msg("server failed to start or exited immediately")
		notify("Server Error", "Server failed to start or exited immediately")
		return nil
	}

	log.Info().Msg("✅ server started successfully")
	return cmd
}

func watch(backendPath, frontendPath string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Error().Err(err).Msg("cannot start file watcher, continuing without watching")
		notify("Watcher Error", "Failed to start file watcher; manual restarts required")
		return
	}
	defer watcher.Close()

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					log.Warn().Msg("file watcher channel closed")
					return
				}
				if event.Op&fsnotify.Write == fsnotify.Write {
					fileChanged(event)
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					log.Warn().Msg("file watcher error channel closed")
					return
				}
				log.Error().Err(err).Msg("error while watching files")
				notify("Watcher Error", fmt.Sprintf("File watching error: %v", err))
			case <-quit:
				log.Info().Msg("stopping file watcher")
				return
			}
		}
	}()

	watchPaths := append(getDirectoryWithSubDirectories(),
		"../frontend/schema_custom.graphql",
		"../frontend/src/__generated__",
	)
	for _, w := range watchPaths {
		if err := watcher.Add(w); err != nil {
			log.Error().Err(err).Str("path", w).Msg("failed to watch path")
			notify("Watcher Error", fmt.Sprintf("Failed to watch path %s: %v", w, err))
		}
	}

	<-quit
}

var debounced = debounce.New(200 * time.Millisecond)

func runSqlChanged() {
	if err := dropDatabase(); err != nil {
		log.Error().Err(err).Msg("sql changed: failed to drop database")
		notify("SQL Error", fmt.Sprintf("Failed to drop database: %v", err))
		return
	}
	if err := runMigrations(); err != nil {
		log.Error().Err(err).Msg("sql changed: failed to run migrations")
		notify("SQL Error", fmt.Sprintf("Failed to run migrations: %v", err))
		return
	}
	if err := runConvertPlugin(); err != nil {
		log.Error().Err(err).Msg("sql changed: failed to run convert plugin")
		notify("SQL Error", fmt.Sprintf("Failed to run convert plugin: %v", err))
		return
	}
	if err := runSeeder(); err != nil {
		log.Error().Err(err).Msg("sql changed: failed to run seeder")
		notify("SQL Error", fmt.Sprintf("Failed to run seeder: %v", err))
		return
	}
	if err := runMergeSchemasWithRelay(); err != nil {
		log.Error().Err(err).Msg("sql changed: failed to run merge schemas with relay")
		notify("SQL Error", fmt.Sprintf("Failed to run merge schemas with relay: %v", err))
		return
	}
	log.Info().Msg("✅ sql changes applied successfully")
	restart <- true
}

func runSchemaChanged() {
	if err := runConvertPlugin(); err != nil {
		log.Error().Err(err).Msg("schema changed: failed to run convert plugin")
		notify("Schema Error", fmt.Sprintf("Failed to run convert plugin: %v", err))
		return
	}
	if err := runMergeSchemasWithRelay(); err != nil {
		log.Error().Err(err).Msg("schema changed: failed to run merge schemas with relay")
		notify("Schema Error", fmt.Sprintf("Failed to run merge schemas with relay: %v", err))
		return
	}
	log.Info().Msg("✅ schema changes applied successfully")
	restart <- true
}

func runSeedChanged() {
	if err := runSeeder(); err != nil {
		log.Error().Err(err).Msg("seed changed: failed to run seeder")
		notify("Seed Error", fmt.Sprintf("Failed to run seeder: %v", err))
		return
	}
	log.Info().Msg("✅ seed changes applied successfully")
}

func runGoChanged() {
	log.Info().Msg("✅ go files changed, triggering restart")
	restart <- true
}

func runMigrationsChanged() {
	if err := runMigrations(); err != nil {
		log.Error().Err(err).Msg("migrations changed: failed to run migrations")
		notify("Migration Error", fmt.Sprintf("Failed to run migrations: %v", err))
		return
	}
	if err := runConvertPlugin(); err != nil {
		log.Error().Err(err).Msg("migrations changed: failed to run convert plugin")
		notify("Migration Error", fmt.Sprintf("Failed to run convert plugin: %v", err))
		return
	}
	log.Info().Msg("✅ migrations applied successfully")
	restart <- true
}

func fileChanged(event fsnotify.Event) {
	log.Debug().Str("file", event.Name).Msg("modified file")

	isGeneratedGo := strings.Contains(event.Name, "generated_") &&
		(strings.Contains(event.Name, ".go") || strings.Contains(event.Name, ".gohtml"))
	if isGeneratedGo || strings.Contains(event.Name, "__generated__/") {
		log.Debug().Msg("generated files changed, skipping")
		return
	}

	switch {
	case strings.Contains(event.Name, ".sql"):
		log.Debug().Msg("sql changed, running migrations + convert plugin")
		debounced(runSqlChanged)
	case strings.Contains(event.Name, ".graphql"):
		log.Debug().Msg("graphql schema changed, running convert & merge schemas with relay")
		debounced(runSchemaChanged)
	case strings.Contains(event.Name, "seed/"):
		log.Debug().Msg("seed files changed, re-running seed.go")
		debounced(runSeedChanged)
	case strings.Contains(event.Name, ".env") ||
		strings.Contains(event.Name, ".go") ||
		strings.Contains(event.Name, ".gohtml"):
		log.Debug().Msg("go or env files changed, restarting server")
		debounced(runGoChanged)
	case strings.Contains(event.Name, "migrations/"):
		log.Debug().Msg("migrations changed, running migrations + convert plugin")
		debounced(runMigrationsChanged)
	}
}
