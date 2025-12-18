# wr

A developer tool for hot-reloading Go/GraphQL projects at WebRidge. Watches for file changes and automatically runs the appropriate build steps.

## Installation

```bash
go install github.com/web-ridge/wr@latest
```

## Usage

Run from your backend directory:

```bash
wr              # Full mode - watch all files
wr --go         # Go-only mode - only restart on .go file changes
wr --no-watch   # Manual mode - no file watcher, keyboard shortcuts only
```

## How it works

```
┌─────────────────────────────────────────────────────────────┐
│                         wr startup                          │
├─────────────────────────────────────────────────────────────┤
│  1. Start MySQL database (docker compose up -d db)          │
│  2. Run migrations (go run migrate.go)                      │
│  3. Run convert plugin (convert/convert.go)                 │
│  4. Run seeder (seed/seed.go)                               │
│  5. Start server (go run server.go)                         │
│  6. Start file watcher                                      │
└─────────────────────────────────────────────────────────────┘
```

### File Watcher Actions

| File Changed | Action |
|--------------|--------|
| `.go` `.gohtml` `.env` | Restart server |
| `.sql` | Drop DB → Migrate → Convert → Seed → Restart |
| `.graphql` | Convert → Merge schemas → Relay |
| `seed/*` | Re-run seeder |
| `migrations/*` | Run migrations → Convert |

### Keyboard Shortcuts

Always available during a session:

| Key | Action |
|-----|--------|
| `r` | Restart server |
| `c` | Run convert |
| `s` | Run seeder |
| `m` | Run migrations |
| `a` | Run all (migrate + convert + seed + restart) |
| `h` | Show help |
| `^C` | Quit |

## Project Structure

wr expects this directory structure:

```
/org/app-name/
├── backend/              ← Run wr from here
│   ├── server.go
│   ├── migrate.go
│   ├── convert/
│   │   └── convert.go
│   ├── seed/
│   │   └── seed.go
│   └── migrations/
├── frontend/
│   ├── schema_custom.graphql
│   └── src/__generated__/
└── docker-compose.yaml
```

## Multi-Instance Support

Each app gets isolated Docker containers based on the parent directory name:

- `/org/photopilot/backend` → containers: `photopilot-db-1`
- `/org/other-app/backend` → containers: `other-app-db-1`

This allows running multiple wr instances for different projects simultaneously.

## Development

### Build

```bash
go build -o wr .
```

### Install locally

```bash
go install .
```
