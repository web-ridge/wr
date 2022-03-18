package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/fsnotify/fsnotify"

	"github.com/urfave/cli/v2"
)

func main() {
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
		log.Fatal(err)
	}
}

func start(c *cli.Context) error {
	organizationName, projectName, basePath := getPathInformation()
	uniqueName := organizationName + "-" + projectName
	runLocalCommand("docker-compose up -d -p " + uniqueName)
	fmt.Println("We're watching for changes in migrations and custom graphql")

	backendPath := path.Join(basePath, organizationName, projectName, "backend")
	frontendPath := path.Join(basePath, organizationName, projectName, "frontend")

	// TODO: get hash of all migration contents
	// TODO: save hash of all migration contents
	// TODO: if hash is different, run migrations + convert plugin
	// TODO: watch migrations folder
	// TODO: watch custom_schema.grapql if changed run convert_plugin
	// TODO:
	//
	watch(c, backendPath, frontendPath)

	return nil
}

func watch(c *cli.Context, backendPath, frontendPath string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer func(watcher *fsnotify.Watcher) {
		err := watcher.Close()
		if err != nil {
			log.Fatal(err)
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
				// log.Println("event:", event)
				if event.Op&fsnotify.Write == fsnotify.Write {
					log.Println("modified file:", event.Name)
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Println("error:", err)
			}
		}
	}()

	// TODO: watch migrations folder
	// TODO: watch custom_schema.grapql if changed run convert_plugin

	err = watcher.Add(path.Join(backendPath, "migrations"))
	checkError(err, "failed to watch migrations folder")
	err = watcher.Add(path.Join(frontendPath, "schema_custom.graphql"))
	checkError(err, "failed to watch custom_schema.graphql")

	<-done
}

func startDocker(c *cli.Context) error {
	// TODO: add
	fmt.Println("boom! I say!")
	return nil
}

func startBackend(c *cli.Context) error {
	// TODO: add
	fmt.Println("boom! I say!")
	return nil
}

func runLocalCommand(command string) {
	cmd := exec.Command("/bin/sh", "-c", command)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		fmt.Println(stderr.String())
		return
	}
	fmt.Println(out.String())
}

func checkError(err error, s string) {
	if err != nil {
		fmt.Println(fmt.Errorf("%v: %v", s, err))
		os.Exit(1)
	}
}

func getPathInformation() (string, string, string) {
	path, err := os.Getwd()
	checkError(err, "get project name")

	directories := strings.Split(path, "/")
	var basePath []string
	for i, directory := range directories {
		basePath = append(basePath, directory)
		if directory == "github.com" {
			organizationName := directories[i+1]
			projectName := directories[i+2]

			// next directory is organization name
			// directory after that is project name
			return organizationName, projectName, strings.Join(basePath, "/")
		}
	}
	return "unknown-org", "unknown-project", "unknown-path"
}
