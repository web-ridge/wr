package helpers

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/rs/zerolog/log"
)

var requiredDatabaseEnvs = []string{
	"DATABASE_USER",
	"DATABASE_PASSWORD",
	"DATABASE_HOST",
	"DATABASE_PORT",
	"DATABASE_NAME",
}

func getConnectionString() string {
	return fmt.Sprintf(`%v:%v@tcp(%v:%v)/%v?multiStatements=%v&parseTime=true&tls=%v`,
		os.Getenv("DATABASE_USER"),
		os.Getenv("DATABASE_PASSWORD"),
		os.Getenv("DATABASE_HOST"),
		os.Getenv("DATABASE_PORT"),
		os.Getenv("DATABASE_NAME"),
		os.Getenv("DATABASE_MULTIPLE_STATEMENTS"),
		os.Getenv("DATABASE_TLS"),
	)
}

func WaitForDatabase() *sql.DB {
	defer func() {
		log.Debug().Msg("database connection is ready :)")
	}()
	for {
		log.Debug().Msg("waiting for database connection")

		for _, requiredEnvName := range requiredDatabaseEnvs {
			if os.Getenv(requiredEnvName) == "" {
				log.Fatal().Str(requiredEnvName, "is required").Msg("could not open database connection")
			}
		}

		// Open handle to database like normal
		db, err := sql.Open("mysql", getConnectionString())
		if err != nil {
			log.Fatal().Err(err).Msg("could not open database connection")
		}

		if err = db.Ping(); err != nil {
			time.Sleep(1 * time.Second)
			continue
		}

		// if we have no errors while pinging db is ready :)
		return db
	}
}
