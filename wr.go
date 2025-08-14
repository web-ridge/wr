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

func main() {
	helpers.ConfigureLogger()

	app := &cli.App{
		Name:   "wr",
		Usage:  "wr is an internal tool to improve developer experience at webRidge",
		Action: start,
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal().Err(err).Msg("cannot run app")
	}
}

func start(c *cli.Context) error {
	log.Info().Msg(`
               _     _____  _     _            
              | |   |  __ \(_)   | |           
 __      _____| |__ | |__) |_  __| | __ _  ___ 
 \ \ /\ / / _ \ '_ \|  _  /| |/ _` + "`" + ` |/ _` + "`" + ` |/ _ \
  \ V  V /  __/ |_) | | \ \| | (_| | (_| |  __/
   \_/\_/ \___|_.__/|_|  \_\_|\__,_|\__,_|\___|
                                    |___/      
`)

	// Set up signal handling for Ctrl+C and termination
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	var dockerCmd *exec.Cmd
	var existingServer *exec.Cmd

	// Cleanup function for graceful shutdown
	defer func() {
		log.Info().Msg("shutting down, cleaning up processes...")
		if existingServer != nil {
			stopServer(existingServer)
		}
		if dockerCmd != nil {
			stopDocker()
		}
		killPortProcess(port)
		close(quit)
	}()

	p, err := setupPaths()
	if err != nil {
		return fmt.Errorf("setup paths: %w", err)
	}

	if err := installDependencies(p.frontend); err != nil {
		return fmt.Errorf("install dependencies: %w", err)
	}

	dockerCmd = startDbInDocker()
	time.Sleep(1 * time.Second)
	db = helpers.WaitForDatabase()

	if err := runInitialSetup(); err != nil {
		return fmt.Errorf("initial setup: %w", err)
	}

	// Start server after initial setup
	killPortProcess(port)
	existingServer = startServerInBackground(true)
	if existingServer == nil {
		log.Error().Msg("initial server start failed, continuing to watch for changes")
	}

	go watch(p.backend, p.frontend)

	// Main loop for restarts and signal handling
	for {
		select {
		case <-restart:
			log.Debug().Msg("restarting backend...")
			if existingServer != nil {
				stopServer(existingServer)
			}
			killPortProcess(port)
			existingServer = startServerInBackground(true)
			if existingServer == nil {
				log.Error().Msg("server restart failed, continuing to watch for changes")
				continue
			}
			log.Debug().Msg("✅ restarted backend")
		case <-sigChan:
			log.Info().Msg("received shutdown signal")
			return nil
		case <-quit:
			return nil
		}
	}
}

func setupPaths() (paths, error) {
	backend, err := os.Getwd()
	if err != nil {
		return paths{}, fmt.Errorf("get current dir: %w", err)
	}
	startPath := filepath.Dir(backend)
	dirs := strings.Split(startPath, string(os.PathSeparator))
	if len(dirs) < 2 {
		return paths{}, fmt.Errorf("invalid path structure")
	}
	return paths{
		backend:  backend,
		frontend: path.Join(startPath, "frontend"),
		orgName:  dirs[len(dirs)-2],
	}, nil
}

func installDependencies(frontendPath string) error {
	for _, fn := range []func() error{
		installBun,
		func() error { return installFrontendDependencies(frontendPath) },
		installPrettier,
		installSqlBoiler,
		installSqlBoilerMysqlDriver,
	} {
		if err := fn(); err != nil {
			return err
		}
	}
	return nil
}

func installBun() error {
	log.Debug().Msg("install bun")
	cmd := exec.Command("npm", "install", "-g", "bun@latest", "--force", "--silent")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func installFrontendDependencies(frontendPath string) error {
	log.Debug().Msg("install frontend dependencies")
	cmd := exec.Command("bun", "install")
	cmd.Dir = frontendPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func installPrettier() error {
	log.Debug().Msg("install prettier")
	cmd := exec.Command("npm", "install", "-g", "prettier@latest", "--force", "--silent")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func installSqlBoiler() error {
	log.Debug().Msg("install sqlboiler")
	cmd := exec.Command("go", "install", "github.com/aarondl/sqlboiler/v4@latest")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func installSqlBoilerMysqlDriver() error {
	log.Debug().Msg("install sqlboiler mysql driver")
	cmd := exec.Command("go", "install", "github.com/aarondl/sqlboiler/v4/drivers/sqlboiler-mysql@latest")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func notify(title, message string) {
	if err := beeep.Notify(title, message, "./icon.png"); err != nil {
		log.Error().Err(err).Msg("could not notify")
	}
}

func stopServer(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		specific.Kill(cmd) // No error return expected
	}
	killPortProcess(port)
}

func startServerInBackground(restart bool) *exec.Cmd {
	for attempt := 1; ; attempt++ {
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

		// Start server in a goroutine to allow retries
		serverStarted := make(chan bool, 1)
		go func() {
			if err := cmd.Run(); err != nil && !strings.Contains(err.Error(), "signal: killed") {
				log.Error().Err(err).Int("attempt", attempt).Msg("failed to run server")
				notify("Server Error", fmt.Sprintf("Failed to run server (attempt %d): %v", attempt, err))
				serverStarted <- false
			} else {
				serverStarted <- true
			}
		}()

		// Wait to verify server startup
		time.Sleep(500 * time.Millisecond)
		if cmd.Process == nil || cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			log.Error().Int("attempt", attempt).Msg("server failed to start or exited immediately")
			notify("Server Error", fmt.Sprintf("Server failed to start or exited immediately (attempt %d)", attempt))
			time.Sleep(time.Second * time.Duration(attempt))
			continue
		}

		// Check if the server started successfully
		select {
		case success := <-serverStarted:
			if !success {
				time.Sleep(time.Second * time.Duration(attempt))
				continue
			}
		case <-time.After(2 * time.Second):
			log.Error().Int("attempt", attempt).Msg("server startup timed out")
			notify("Server Error", fmt.Sprintf("Server startup timed out (attempt %d)", attempt))
			if cmd.Process != nil {
				specific.Kill(cmd)
			}
			time.Sleep(time.Second * time.Duration(attempt))
			continue
		}

		log.Info().Int("attempt", attempt).Msg("✅ server started successfully")
		return cmd
	}
}

func startDbInDocker() *exec.Cmd {
	cmd := exec.Command("docker", "compose", "up", "-d", "db")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Error().Err(err).Msg("failed to start db")
		notify("DB Error", "failed to start db")
	}
	return cmd
}

func stopDocker() {
	cmd := exec.Command("docker", "compose", "down")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Error().Err(err).Msg("failed to stop docker containers")
	}
}

func killPortProcess(port string) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell", "-Command", fmt.Sprintf("Stop-Process -Id (Get-NetTCPConnection -LocalPort %s).OwningProcess -Force", port))
	} else {
		cmd = exec.Command("bash", "-c", fmt.Sprintf("lsof -i tcp:%s | grep LISTEN | awk '{print $2}' | xargs kill -9", port))
	}
	if err := cmd.Run(); err != nil {
		log.Debug().Err(err).Msg("error killing port process")
	}
}

func runInitialSetup() error {
	for _, fn := range []func() error{
		dropDatabase,
		runMigrations,
		runMergeSchemasWithRelay,
		runConvertPlugin,
		runSeeder,
	} {
		if err := fn(); err != nil {
			return err
		}
	}
	log.Info().Msg("Done migrating :)")
	return nil
}

func runMigrations() error {
	log.Debug().Msg("run migrations")
	cmd := exec.Command("go", "run", "migrate.go")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func dropDatabase() error {
	log.Debug().Msg("drop db")
	name := os.Getenv("DATABASE_NAME")
	_, err := db.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS `%v`", name))
	if err != nil {
		return fmt.Errorf("drop database: %w", err)
	}
	_, err = db.Exec(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%v`", name))
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	log.Debug().Msg("✅ dropped db")
	return nil
}

func runConvertPlugin() error {
	log.Debug().Msg("run convert/convert.go")
	cmd := exec.Command("go", "run", "convert.go")
	cmd.Dir = "./convert"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runMergeSchemasWithRelay() error {
	if err := runMergeSchemas(); err != nil {
		return err
	}
	return runRelay()
}

func runMergeSchemas() error {
	log.Debug().Msg("run merge-schemas")
	cmd := exec.Command("bun", "run", "merge-schemas")
	cmd.Dir = "../frontend"
	return cmd.Run()
}

func runRelay() error {
	log.Debug().Msg("run relay.dev")
	cmd := exec.Command("bun", "run", "relay")
	cmd.Dir = "../frontend"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runSeeder() error {
	log.Debug().Msg("run seed/seed.go")
	cmd := exec.Command("go", "run", "seed.go")
	cmd.Dir = "./seed"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "DATABASE_DEBUG=false")
	return cmd.Run()
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

func getDirectoryWithSubDirectories() []string {
	var dirs []string
	dirs = append(dirs, "./")
	if err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Error().Err(err).Msg("walking files")
			return err
		}
		if info.IsDir() && !strings.Contains(path, "models/") && !strings.Contains(path, ".idea") {
			dirs = append(dirs, path)
		}
		return nil
	}); err != nil {
		log.Error().Err(err).Msg("could not get dir with sub dirs")
	}
	return dirs
}
