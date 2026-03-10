# syntax=docker/dockerfile:1

# ============================================================
# Build stage
# ============================================================
FROM golang:1.23-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /src

# Download dependencies (cached layer)
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s -extldflags '-static'" \
    -o /biocom \
    ./cmd/biocom

# ============================================================
# Final stage - minimal runtime image
# ============================================================
FROM scratch

# Import CA certificates and timezone data from builder
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Copy binary
COPY --from=builder /biocom /biocom

# Create upload directories (will be mounted as volumes)
# Note: actual directories created at runtime via config

# Run as non-root user (UID 65534 = nobody)
USER 65534:65534

ENTRYPOINT ["/biocom"]
