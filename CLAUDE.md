# WR - WebRidge Developer Tool

## What is this?
`wr` is an internal CLI tool that improves developer experience for WebRidge Go/GraphQL projects. It watches for file changes and automatically runs the appropriate build steps.

## Project Structure
```
/org/app-name/
├── backend/          ← Run `wr` from here
│   ├── server.go     # Main server entry point
│   ├── migrate.go    # Database migrations runner
│   ├── convert/      # GraphQL schema converter
│   │   └── convert.go
│   ├── seed/         # Database seeder
│   │   └── seed.go
│   ├── models/       # Generated SQLBoiler models (don't edit)
│   └── migrations/   # SQL migration files
├── frontend/
│   ├── schema_custom.graphql  # Custom GraphQL schema
│   └── src/__generated__/     # Generated Relay files
└── docker-compose.yaml        # Database container config
```

## How it works
1. Starts MySQL database via Docker Compose
2. Runs initial setup (migrations, convert, seed)
3. Starts the Go server in background
4. Watches for file changes and triggers appropriate actions:
   - `.go` / `.gohtml` / `.env` → Restart server
   - `.sql` → Drop DB + Migrate + Convert + Seed + Restart
   - `.graphql` → Convert + Merge schemas + Relay
   - `seed/` changes → Re-run seeder
   - `migrations/` changes → Run migrations + Convert

## CLI Usage
```bash
wr              # Full mode: watch all files, run all actions
wr --go         # Go-only mode: only restart on .go file changes
wr --no-watch   # Manual mode: no file watcher, use keyboard shortcuts
```

## Keyboard Shortcuts (always available)
- `r` - Restart server
- `c` - Run convert (GraphQL schema → Go code)
- `s` - Run seeder
- `m` - Run migrations
- `a` - Run all (migrate + convert + seed + restart)
- `h` - Show help

## Key Functions
- `startServerInBackground()` - Runs `go run server.go` with process group for cleanup
- `runConvertPlugin()` - Runs `convert/convert.go` to generate Go from GraphQL
- `runMigrations()` - Runs `migrate.go` to apply SQL migrations
- `runSeeder()` - Runs `seed/seed.go` to populate test data
- `runMergeSchemasWithRelay()` - Merges GraphQL schemas and runs Relay compiler

## Dependencies
- Go 1.21+
- Bun (for frontend tooling)
- Docker (for database)
- SQLBoiler (Go ORM generator)
- Prettier (code formatting)

## File Watcher
Uses native macOS FSEvents on Darwin, falls back to fsnotify on other platforms.
The watcher debounces file changes (200ms) to avoid duplicate runs.

## Common Issues
- **Server won't restart**: Press `r` to manually restart
- **Database out of sync**: Press `a` to run full rebuild
