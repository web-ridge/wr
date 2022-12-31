package main

import (
	"database/sql"
	"fmt"
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
	checkErrorWithFatal("cant get current dir", err)
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
	runMergeSchemasWithRelay()
	runConvertPlugin()
	runSeeder()

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
		if err != nil {
			checkServerError(err)
		}
		defer func() {
			log.Debug().Msg("kill server")
			err = cmd.Process.Kill()
			alreadyKilled := strings.Contains(err.Error(), "process already finished")
			if !alreadyKilled {
				checkServerError(err)
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
	log.Debug().Msg("run migrations")
	cmd := exec.Command("go", "run", "migrate.go")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	checkError("failed to run migrations", err)
	log.Debug().Msg("✅ done migrating!")
	return
}

func dropDatabase() {
	log.Debug().Msg("drop db")
	name := os.Getenv("DATABASE_NAME")
	var err error
	_, err = db.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS `%v`", name))
	checkErrorWithFatal("could not drop database", err)
	_, err = db.Exec(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%v`", name))
	checkErrorWithFatal("could not create database", err)
	log.Debug().Msg("✅ dropped db!")
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

func runMergeSchemasWithRelay() {
	runMergeSchemas()
	runRelay()
	// forceIndexGeneratedDirectory()
}

// forceIndexGeneratedDirectory forces the frontend IDE to index the generated dir faster
//func forceIndexGeneratedDirectory() {
//	prefix := "wr-version-index-"
//
//	generatedPath := "../frontend/src/__generated__"
//	err := filepath.WalkDir(generatedPath, func(s string, d fs.DirEntry, e error) error {
//		if strings.HasPrefix(s, prefix) {
//			checkError("could not remove index", os.Remove(s))
//		}
//		return nil
//	})
//
//	timestamp := strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
//	f, err := os.Create(generatedPath + "/" + prefix + timestamp)
//	checkError("could not close file", f.Close())
//	checkError("could not force index of relay.dev (new)", err)
//}

func runMergeSchemas() {
	log.Debug().Msg("run merge-schemas")

	cmd := exec.Command("yarn", "merge-schemas")
	cmd.Dir = "../frontend"
	// cmd.Stdout = os.Stdout
	// cmd.Stderr = os.Stderr
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
					// log.Debug().Str("file", event.Name).Msg("modified file")
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
	// fmt.Println(filesOrDirectoriesToWatch)
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
	runMergeSchemasWithRelay()
	restart <- true
}

func runSeedChanged() {
	runSeeder()
}

func runGoChanged() {
	restart <- true
}

func runGeneratedChanged() {
	// forceIndexGeneratedDirectory()
}

func runMigrationsChanged() {
	runMigrations()
	runConvertPlugin()
	restart <- true
}

func fileChanged(event fsnotify.Event) {
	envChanged := strings.Contains(event.Name, ".env")
	goChanged := strings.Contains(event.Name, ".go") || strings.Contains(event.Name, ".gohtml")
	sqlChanged := strings.Contains(event.Name, ".sql")
	schemaChanged := strings.Contains(event.Name, ".graphql")
	seedChanged := strings.Contains(event.Name, "/seed/")
	generatedChanged := strings.Contains(event.Name, "/__generated__/")
	migrationsChanged := strings.Contains(event.Name, "/migrations/")
	goGeneratedChanged := strings.Contains(event.Name, "generated_") && goChanged
	// we only change models from here so we don't need to subscribe
	if goGeneratedChanged {
		return
	}

	log.Debug().
		Str("name", event.Name).
		Bool("sqlChanged", sqlChanged).
		Bool("schemaChanged", schemaChanged).
		Bool("goChanged", goChanged).
		Bool("envChanged", envChanged).
		Bool("generatedChanged", generatedChanged).
		Bool("migrationsChanged", migrationsChanged).
		Msg("changed file")
	switch true {
	case generatedChanged:
		debounced(runGeneratedChanged)
	case sqlChanged:
		debounced(runSqlChanged)
	case schemaChanged:
		debounced(runSchemaChanged)
	case seedChanged:
		debounced(runSeedChanged)
	case goChanged, envChanged:
		debounced(runGoChanged)
	case migrationsChanged:
		debounced(runMigrationsChanged)
	}
}

func startDbInDocker() *exec.Cmd {
	cmd := exec.Command("docker-compose", "up", "-d", "db")
	// cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	checkError("failed to start db", cmd.Run())
	return cmd
}

func checkError(s string, err error) {
	if err != nil {
		notify("Error 🔥🔥🔥", s)
		log.Error().Err(err).Msg("🔥🔥🔥 " + s)
	}
}

func checkErrorWithFatal(s string, err error) {
	if err != nil {
		notify("Fatal Error 🔥🔥🔥", s)
		log.Fatal().Err(err).Msg("🔥🔥🔥 " + s)
	}
}

func getDirectoryWithSubDirectories() []string {
	var a []string
	a = append(a, "./")
	err := filepath.Walk(".",
		func(path string, info os.FileInfo, err error) error {
			checkErrorWithFatal("walking files", err)

			if info.IsDir() {
				if strings.Contains(path, "models/") {
					return nil
				}
				if strings.Contains(path, ".idea") {
					return nil
				}
				a = append(a, path)
			}

			return nil
		})
	checkErrorWithFatal("could not get dir with sub dirs", err)
	return a
}
