# BIOCOM Discord Bot

Production-grade Discord bot for ARMA 3 mission and preset management.

## Features

- **Mission Upload** (`/upload_mission`) - Upload and broadcast `.pbo` mission files
- **Preset Upload** (`/upload_preset`) - Upload and broadcast `.html` preset files
- **Intercept** (`/intercept`) - Post messages to channels as BIOCOM
- **Containers** (`/containers`) - List running Docker containers (admin only)
- **Ping** (`/ping`) - Health check

## Requirements

- Go 1.23+ (for local development)
- Docker & Docker Compose (for deployment)
- Discord Bot Token with application commands scope

## Quick Start

### Local Development

```bash
# Install dependencies
make deps

# Set environment variables
export DISCORD_TOKEN="your-bot-token"
export SERVER_ID="your-guild-id"

# Run locally
make run
```

### Docker Deployment

```bash
# Create .env file
cat > .env << EOF
DISCORD_TOKEN=your-bot-token
SERVER_ID=your-guild-id
EOF

# Create upload directories
mkdir -p uploads/missions uploads/presets

# Build and run
make docker-rebuild
```

## Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `DISCORD_TOKEN` | Discord bot token | Required |
| `SERVER_ID` | Discord guild ID | Required |
| `UPLOAD_BASE` | Base path for uploads | `/app/uploads` |
| `DOCKER_HOST` | Docker socket path | `unix:///var/run/docker.sock` |

## Commands

| Command | Description | Required Role |
|---------|-------------|---------------|
| `/ping` | Check bot status | None |
| `/intercept` | Post message to channel | Zeus |
| `/upload_mission` | Upload mission file | Zeus |
| `/upload_preset` | Upload preset file | Zeus |
| `/containers` | List containers | Server Admin + Administrator |

## Project Structure

```
.
├── cmd/biocom/         # Application entrypoint
│   └── main.go
├── internal/           # Private packages
│   ├── bot/           # Discord bot logic
│   │   ├── bot.go
│   │   └── commands.go
│   ├── config/        # Configuration
│   │   └── config.go
│   └── docker/        # Container API
│       └── client.go
├── Dockerfile         # Multi-stage production build
├── compose.yaml       # Docker Compose config
├── Makefile          # Build automation
└── go.mod            # Go module definition
```

## Building

```bash
# Build binary
make build

# Run tests
make test

# Run linter
make lint

# Build Docker image
make docker-build
```

## License

MIT
