# syntax=docker/dockerfile:1

# ============================================================
# Build stage
# ============================================================
FROM golang:1.25-alpine AS builder

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

# Create minimal passwd/group for scratch (so Docker can resolve user names)
RUN printf 'root:x:0:0:root:/root:/sbin/nologin\nnobody:x:65534:65534:nobody:/nonexistent:/sbin/nologin\n' > /etc/passwd.scratch \
 && printf 'root:x:0:\nnobody:x:65534:\n' > /etc/group.scratch

# ============================================================
# Final stage - minimal runtime image
# ============================================================
FROM scratch

# Import CA certificates and timezone data from builder
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Minimal passwd/group so Docker can resolve user names (scratch has none)
COPY --from=builder /etc/passwd.scratch /etc/passwd
COPY --from=builder /etc/group.scratch /etc/group

# Copy binary
COPY --from=builder /biocom /biocom

ENTRYPOINT ["/biocom"]
