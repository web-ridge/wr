package main

import (
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/bep/debounce"
	"github.com/gen2brain/beeep"
	"github.com/web-ridge/wr/helpers"

	_ "github.com/joho/godotenv/autoload"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog/log"
	"github.com/urfave/cli/v2"
)

// https://stackoverflow.com/questions/36419054/go-projects-main-goroutine-sleep-forever
var (
	quit    = make(chan bool)
	restart = make(chan bool)
	port    = os.Getenv("PORT")
)
var db *sql.DB

func main() {
	// let us have colored logs
	helpers.ConfigureLogger()

	app := &cli.App{
		//Flags: []cli.Flag {
		//	&cli.StringFlag{
		//		Name: "lang",
		//		Value: "english",
		//		Usage: "language for the greeting",
		//	},
		//},
		Name:   "wr",
		Usage:  "wr is an internal tool to improve developer experience at webRidge",
		Action: start,
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal().Err(err).Msg("can not run app")
	}

	// to quit program from somewhere else use close(quit)
	<-quit
}

func start(c *cli.Context) error {
	fmt.Println("               _     _____  _     _            \n              | |   |  __ \\(_)   | |           \n __      _____| |__ | |__) |_  __| | __ _  ___ \n \\ \\ /\\ / / _ \\ '_ \\|  _  /| |/ _` |/ _` |/ _ \\\n  \\ V  V /  __/ |_) | | \\ \\| | (_| | (_| |  __/\n   \\_/\\_/ \\___|_.__/|_|  \\_\\_|\\__,_|\\__, |\\___|\n                                     __/ |     \n                                    |___/   ") //nolint:lll
	fmt.Println("")

	backendPath, err := os.Getwd()
	checkError("cant get current dir", err)
	startPath := filepath.Dir(backendPath)
	directories := strings.Split(startPath, "/")
	organizationName := directories[len(directories)-2]

	log.Debug().Str("organization", organizationName).Msg("starting backend and dependencies")

	frontendPath := path.Join(startPath, "frontend")

	// first we start the database
	go startDbInDocker()

	// wait till the db is started
	time.Sleep(1 * time.Second)
	db = helpers.WaitForDatabase()

	dropDatabase()
	runMigrations()

	runSeeder()
	runConvertPlugin()

	// start watching migrations/code
	go watch(backendPath, frontendPath)

	// start server and wait for restarts
	killPortProcess(port)
	existingServer := startServerInBackground(false)
	for <-restart {
		log.Debug().Msg("restarting backend...")
		stopServer(existingServer)
		existingServer = startServerInBackground(true)
		log.Debug().Msg("✅ restarted backend..")
	}

	// stop the server
	stopServer(existingServer)

	return nil
}

func notify(title, message string) {
	err := beeep.Notify(title, message, "./icon.png")
	checkError("could not notify", err)
}

func stopServer(existingServer *exec.Cmd) {
	// https://stackoverflow.com/a/68179972/2508481
	// Send kill signal to the process group instead of single process (it gets the same value as the PID, only negative)
	if existingServer != nil && existingServer.Process != nil {
		err := syscall.Kill(-existingServer.Process.Pid, syscall.SIGKILL)
		checkError("can not stop server", err)
	}

	killPortProcess(port)
}

func startServerInBackground(restart bool) *exec.Cmd {
	cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf("WR_RESTART=%v go run server.go", restart))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// https://stackoverflow.com/a/68179972/2508481
	// Request the OS to assign process group to the new process, to which all its children will belong
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	go func() {
		err := cmd.Run()
		checkServerError(err)
		defer func() {
			log.Debug().Msg("kill server")
			err = cmd.Process.Kill()
			alreadyKilled := strings.Contains(err.Error(), "process already finished")
			if !alreadyKilled {
				checkError("can not kill server", err)
			}
		}()
	}()

	return cmd
}

func checkServerError(err error) {
	if err != nil && strings.Contains(err.Error(), "signal: killed") {
		return
	}
	checkError("failed to run server", err)
}

func runMigrations() {
	cmd := exec.Command("go", "run", "migrate.go")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	checkError("failed to run migrations", err)
	return
}

func dropDatabase() {
	name := os.Getenv("DATABASE_NAME")
	var err error
	_, err = db.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS `%v`", name))
	checkError("could not drop database", err)
	_, err = db.Exec(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%v`", name))
	checkError("could not create database", err)
}

func runConvertPlugin() {
	log.Debug().Msg("start convert/convert.go")

	cmd := exec.Command("go", "run", "convert.go")
	cmd.Dir = "./convert"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	checkError("failed to run convert/convert.go", err)

	log.Debug().Msg("✅ done convert/convert.go")
}

func killPortProcess(port string) {
	if runtime.GOOS == "windows" {
		command := fmt.Sprintf("(Get-NetTCPConnection -LocalPort %s).OwningProcess -Force", port)
		execKillCommand(exec.Command("Stop-Process", "-Id", command))
	} else {
		command := fmt.Sprintf("lsof -i tcp:%s | grep LISTEN | awk '{print $2}' | xargs kill -9", port)
		execKillCommand(exec.Command("bash", "-c", command))
	}
}

// Execute command and return exited code.
func execKillCommand(cmd *exec.Cmd) {
	var waitStatus syscall.WaitStatus
	if err := cmd.Run(); err != nil {
		if err != nil {
			os.Stderr.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
		}
		if exitError, ok := err.(*exec.ExitError); ok {
			waitStatus = exitError.Sys().(syscall.WaitStatus)
			fmt.Printf("Error during killing (exit code: %s)\n", []byte(fmt.Sprintf("%d", waitStatus.ExitStatus())))
		}
	} else {
		waitStatus = cmd.ProcessState.Sys().(syscall.WaitStatus)
		fmt.Printf("Port successfully killed (exit code: %s)\n", []byte(fmt.Sprintf("%d", waitStatus.ExitStatus())))
	}
}

func runRelayGenerator() {
	runMergeSchemas()
	runRelay()
	forceIndexGeneratedDirectory()
}

// forceIndexGeneratedDirectory forces the frontend IDE to index the generated dir faster
func forceIndexGeneratedDirectory() {
	prefix := "wr-version-index-"
	files := glob("../frontend/src/__generated__", func(s string) bool {
		return strings.HasPrefix(s, prefix)
	})
	for _, file := range files {
		log.Debug().Str("file", file).Msg("loop version index")
		checkError("could not force index of relay.dev (existing)", os.Rename(file, "./wr-version-index-"))
	}
	if len(files) == 0 {
		f, err := os.Create(prefix + "0")
		checkError("could not close file", f.Close())
		checkError("could not force index of relay.dev (new)", err)
	}
}

func runMergeSchemas() {
	log.Debug().Msg("run merge-schemas")

	cmd := exec.Command("yarn", "merge-schemas")
	cmd.Dir = "../frontend"
	// cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	checkError("failed to run convert/convert.go", err)

	log.Debug().Msg("✅ done with merge-schemas")
}

func runRelay() {
	log.Debug().Msg("run relay.dev")

	cmd := exec.Command("yarn", "relay")
	cmd.Dir = "../frontend"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	checkError("failed to run convert/convert.go", err)

	log.Debug().Msg("✅ done with relay.dev")
}

func runSeeder() {
	log.Debug().Msg("start seed/seed.go")

	cmd := exec.Command("go", "run", "seed.go")
	cmd.Dir = "./seed"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "DATABASE_DEBUG=false")
	err := cmd.Run()
	checkError("failed to run seed/seed.go", err)

	log.Debug().Msg("✅ done seed/seed.go")
}

func watch(backendPath, frontendPath string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal().Err(err).Msg("can not start file watcher")
	}
	defer func(watcher *fsnotify.Watcher) {
		err := watcher.Close()
		if err != nil {
			log.Fatal().Err(err).Msg("could not stop file watcher")
		}
	}(watcher)

	done := make(chan bool)
	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&fsnotify.Write == fsnotify.Write {
					log.Debug().Str("file", event.Name).Msg("modified file")
					fileChanged(event)
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Err(err).Msg("error while watching files")
			}
		}
	}()

	filesOrDirectoriesToWatch := getDirectoryWithSubDirectories()

	filesOrDirectoriesToWatch = append(filesOrDirectoriesToWatch, []string{
		"../frontend/schema_custom.graphql",
		"../frontend/src/__generated__",
	}...)
	fmt.Println(filesOrDirectoriesToWatch)
	for _, w := range filesOrDirectoriesToWatch {
		err = watcher.Add(w)
		checkError(fmt.Sprintf("failed to watch %v", w), err)
	}

	<-done
}

var debounced = debounce.New(200 * time.Millisecond)

func runSqlChanged() {
	dropDatabase()
	runMigrations()
	runConvertPlugin()
	runSeeder()
	restart <- true
}

func runSchemaChanged() {
	runConvertPlugin()
	runRelayGenerator()
	restart <- true
}

func runSeedChanged() {
	runSeeder()
}

func runGoChanged() {
	restart <- true
}

func runGeneratedChanged() {
	forceIndexGeneratedDirectory()
}

func runMigrationsChanged() {
	runMigrations()
	runConvertPlugin()
	restart <- true
}

func fileChanged(event fsnotify.Event) {
	modelsChanged := strings.Contains(event.Name, "/models/")
	envChanged := strings.Contains(event.Name, ".env")
	goChanged := strings.Contains(event.Name, ".go")
	sqlChanged := strings.Contains(event.Name, ".sql")
	schemaChanged := strings.Contains(event.Name, ".graphql")
	seedChanged := strings.Contains(event.Name, "/seed/")
	generatedChanged := strings.Contains(event.Name, "/__generated__/")
	migrationsChanged := strings.Contains(event.Name, "/migrations/")
	// we only change models from here so we don't need to subscribe
	if modelsChanged {
		return
	}

	log.Debug().Str("name", event.Name).Msg("changed file")
	switch true {
	case sqlChanged:
		debounced(runSqlChanged)
	case schemaChanged:
		debounced(runSchemaChanged)
	case seedChanged:
		debounced(runSeedChanged)
	case goChanged, envChanged:
		debounced(runGoChanged)
	case generatedChanged:
		debounced(runGeneratedChanged)
	case migrationsChanged:
		debounced(runMigrationsChanged)
	}
}

func startDbInDocker() *exec.Cmd {
	cmd := exec.Command("docker-compose", "up", "db")
	// cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	checkError("failed to start db", cmd.Run())
	return cmd
}

func checkError(s string, err error) {
	if err != nil {
		notify("Error 🔥🔥🔥", s)
		log.Fatal().Err(err).Msg("🔥🔥🔥 " + s)
	}
}

func glob(root string, fn func(string) bool) []string {
	var files []string
	filepath.WalkDir(root, func(s string, d fs.DirEntry, e error) error {
		if fn(s) {
			files = append(files, s)
		}
		return nil
	})
	return files
}

func getDirectoryWithSubDirectories() []string {
	var a []string
	a = append(a, "./")
	err := filepath.Walk(".",
		func(path string, info os.FileInfo, err error) error {
			checkError("walking files", err)
			if info.IsDir() {
				a = append(a, path)
			}

			return nil
		})
	checkError("could not get dir with sub dirs", err)
	return a
}
