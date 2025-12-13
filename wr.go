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
		log.Error().Err(err).Msg("runInitialSetup failed, continuing to watch for changes")
	}

	// Start server after initial setup
	killPortProcess(port)
	existingServer = startServerInBackground(true)
	if existingServer == nil {
		log.Error().Msg("initial server start failed, continuing to watch for changes")
	}

	if useNativeWatcher() {
		go watchNative(p.backend, p.frontend)
	} else {
		go watch(p.backend, p.frontend)
	}

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
	// Only install Bun if it is not already installed
	if _, err := exec.LookPath("bun"); err == nil {
		log.Debug().Msg("bun already installed, skipping installation")
		return nil
	}
	log.Debug().Msg("install bun")
	cmd := exec.Command("sh", "-c", "curl -fsSL https://bun.com/install | bash")
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

func sendNotification(title, message string) {
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
	killPortProcess(port)

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/C", fmt.Sprintf("set WR_RESTART=%v && go run server.go", restart))
	} else {
		cmd = exec.Command("/bin/sh", "-c", fmt.Sprintf("WR_RESTART=%v go run server.go", restart))
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // Ensure process group is set for proper cleanup

	go func() {
		if err := cmd.Run(); err != nil && !strings.Contains(err.Error(), "signal: killed") {
			sendNotification("Server Error", fmt.Sprintf("failed to run server: %v", err))
			log.Error().Err(err).Msg("failed to run server")
		}
	}()

	// Wait briefly to check if the server started successfully
	time.Sleep(500 * time.Millisecond)
	if cmd.Process == nil || cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		log.Error().Msg("server failed to start or exited immediately")
		return nil
	}

	return cmd
}

func startDbInDocker() *exec.Cmd {
	cmd := exec.Command("docker", "compose", "up", "-d", "db")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		sendNotification("DB Error", "failed to start db")
		log.Fatal().Err(err).Msg("failed to start db")
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
		runConvertPlugin,
		runMergeSchemasWithRelay,
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
		log.Fatal().Err(err).Msg("cannot start file watcher")
	}
	defer watcher.Close()

	done := make(chan bool)
	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&fsnotify.Write == fsnotify.Write ||
					event.Op&fsnotify.Create == fsnotify.Create ||
					event.Op&fsnotify.Rename == fsnotify.Rename {
					fileChanged(event)
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Error().Err(err).Msg("error while watching files")
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
		}
	}

	<-done
}

var debounced = debounce.New(200 * time.Millisecond)

func runSqlChanged() {
	if err := dropDatabase(); err != nil {
		log.Fatal().Err(err).Msg("sql changed: drop database")
	}
	if err := runMigrations(); err != nil {
		log.Fatal().Err(err).Msg("sql changed: run migrations")
	}
	if err := runConvertPlugin(); err != nil {
		log.Fatal().Err(err).Msg("sql changed: run convert plugin")
	}
	if err := runSeeder(); err != nil {
		log.Fatal().Err(err).Msg("sql changed: run seeder")
	}
	if err := runMergeSchemasWithRelay(); err != nil {
		log.Fatal().Err(err).Msg("sql changed: run merge schemas with relay")
	}
	restart <- true
}

func runSchemaChanged() {
	if err := runConvertPlugin(); err != nil {
		log.Fatal().Err(err).Msg("schema changed: run convert plugin")
	}
	if err := runMergeSchemasWithRelay(); err != nil {
		log.Fatal().Err(err).Msg("schema changed: run merge schemas with relay")
	}
	restart <- true
}

func runSeedChanged() {
	if err := runSeeder(); err != nil {
		log.Fatal().Err(err).Msg("seed changed: run seeder")
	}
}

func runGoChanged() {
	restart <- true
}

func runMigrationsChanged() {
	if err := runMigrations(); err != nil {
		log.Fatal().Err(err).Msg("migrations changed: run migrations")
	}
	if err := runConvertPlugin(); err != nil {
		log.Fatal().Err(err).Msg("migrations changed: run convert plugin")
	}
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
		log.Debug().Msg("sql changed, run migrations + convert plugin")
		debounced(runSqlChanged)
	case strings.Contains(event.Name, ".graphql"):
		log.Debug().Msg("run convert & merge schemas with relay")
		debounced(runSchemaChanged)
	case strings.Contains(event.Name, "seed/"):
		log.Debug().Msg("re-run seed.go")
		debounced(runSeedChanged)
	case strings.Contains(event.Name, ".env") ||
		strings.Contains(event.Name, ".go") ||
		strings.Contains(event.Name, ".gohtml"):
		log.Debug().Msg("restart server")
		debounced(runGoChanged)
	case strings.Contains(event.Name, "migrations/"):
		log.Debug().Msg("run migrations + convert plugin")
		debounced(runMigrationsChanged)
	}
}

func getDirectoryWithSubDirectories() []string {
	var dirs []string
	dirs = append(dirs, "./")
	if err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Fatal().Err(err).Msg("walking files")
		}
		if info.IsDir() && !strings.Contains(path, "models/") && !strings.Contains(path, ".idea") {
			dirs = append(dirs, path)
		}
		return nil
	}); err != nil {
		log.Fatal().Err(err).Msg("could not get dir with sub dirs")
	}
	return dirs
}
