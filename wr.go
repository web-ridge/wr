package main

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

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
)

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

	notify("test", "test")

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
	db := helpers.WaitForDatabase()

	dropDatabase(db)
	runMigrations()

	runSeeder()
	runConvertPlugin()

	// start watching migrations/code
	go watch(db, backendPath, frontendPath)

	// start server and wait for restarts
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
	err := beeep.Notify(title, message, "assets/information.png")
	checkError("could not notify", err)
}

func stopServer(existingServer *exec.Cmd) {
	// https://stackoverflow.com/a/68179972/2508481
	// Send kill signal to the process group instead of single process (it gets the same value as the PID, only negative)
	err := syscall.Kill(-existingServer.Process.Pid, syscall.SIGKILL)
	checkError("can not stop server", err)
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

func dropDatabase(db *sql.DB) {
	name := os.Getenv("DATABASE_NAME")
	var err error
	_, err = db.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS `%v`", name))
	checkError("could not drop database", err)
	_, err = db.Exec(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%v`", name))
	checkError("could not create database", err)
}

func runConvertPlugin() {
	log.Debug().Msg("start convert/convert.go")

	cmd := exec.Command("/bin/sh", "-c", "cd convert", "go", "run", "convert.go")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	checkError("failed to run convert/convert.go", err)

	log.Debug().Msg("✅ done convert/convert.go")
}

func runRelayGenerator(backendPath string) {
}

func runSeeder() {
	log.Debug().Msg("start seed/seed.go")

	cmd := exec.Command("/bin/sh", "-c", "cd seed", "go", "run", "seed.go")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	checkError("failed to run seed/seed.go", err)

	log.Debug().Msg("✅ done seed/seed.go")
}

func haveMigrationsChanged() bool {
	migrationsPath := path.Join("./")
	migrationsHashPath := path.Join(migrationsPath, ".wr")
	nextMigrationHash, err := MD5AllString(migrationsPath)
	checkError("error getting hash of migrations", err)

	// TODO: get hash of all migration contents
	currentMigrationsHash, err := os.ReadFile(migrationsHashPath)

	changed := string(currentMigrationsHash) != nextMigrationHash
	if changed {
		// TODO: run migration here + write new hash to file
		err = os.WriteFile(migrationsHashPath, []byte(nextMigrationHash), 0o644)
		checkError("error writing hash of migrations", err)
	}
	return changed
}

func watch(db *sql.DB, backendPath, frontendPath string) {
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
					fileChanged(db, event)
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Err(err).Msg("error while watching files")
			}
		}
	}()

	// TODO: watch migrations folder
	// TODO: watch custom_schema.grapql if changed run convert_plugin

	filesOrDirectoriesToWatch := []string{
		backendPath,
		path.Join(frontendPath, "schema_custom.graphql"),
	}
	for _, w := range filesOrDirectoriesToWatch {
		err = watcher.Add(backendPath)
		checkError(fmt.Sprintf("failed to watch %v", w), err)
	}

	<-done
}

func fileChanged(db *sql.DB, event fsnotify.Event) {
	envChanged := strings.Contains(event.Name, ".env")
	goChanged := strings.Contains(event.Name, ".go")
	sqlChanged := strings.Contains(event.Name, ".sql")
	schemaChanged := strings.Contains(event.Name, ".graphql")

	switch true {
	case sqlChanged:
		dropDatabase(db)
		runMigrations()
		runConvertPlugin()
		runSeeder()
		restart <- true
	case schemaChanged:
		runConvertPlugin()
		restart <- true
	case goChanged, envChanged:
		restart <- true
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
		log.Fatal().Err(err).Msg("🔥🔥🔥 " + s)
	}
}

func MD5AllString(root string) (string, error) {
	m, err := MD5All(root)
	if err != nil {
		return "", err
	}
	var values []string
	for p, v := range m {
		k := strings.TrimPrefix(p, root+"/")
		pass := hex.EncodeToString(v[:])
		values = append(values, k+"="+pass)
	}
	sort.Strings(values)
	return strings.Join(values, "\n"), nil
}

// MD5All reads all the files in the file tree rooted at root and returns a map
// from file path to the MD5 sum of the file's contents.  If the directory walk
// fails or any read operation fails, MD5All returns an error.
func MD5All(root string) (map[string][md5.Size]byte, error) {
	m := make(map[string][md5.Size]byte)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := ioutil.ReadFile(path)
		if err != nil {
			return err
		}
		m[path] = md5.Sum(data)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}
