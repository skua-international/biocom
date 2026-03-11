# syntax=docker/dockerfile:1

# ============================================================
# Build stage (shared)
# ============================================================
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /src

COPY . .

# Build bot binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=vendor \
    -ldflags="-w -s -extldflags '-static'" \
    -o /biocom \
    ./cmd/biocom

# Build watchdog binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=vendor \
    -ldflags="-w -s -extldflags '-static'" \
    -o /watchdog \
    ./cmd/watchdog

# Minimal passwd/group for scratch
RUN printf 'root:x:0:0:root:/root:/sbin/nologin\nnobody:x:65534:65534:nobody:/nonexistent:/sbin/nologin\n' > /etc/passwd.scratch \
 && printf 'root:x:0:\nnobody:x:65534:\n' > /etc/group.scratch

# ============================================================
# Runtime: biocom bot
# ============================================================
FROM scratch AS biocom

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /etc/passwd.scratch /etc/passwd
COPY --from=builder /etc/group.scratch /etc/group
COPY --from=builder /biocom /biocom

ENTRYPOINT ["/biocom"]

# ============================================================
# Runtime: watchdog
# ============================================================
FROM scratch AS watchdog

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /etc/passwd.scratch /etc/passwd
COPY --from=builder /etc/group.scratch /etc/group
COPY --from=builder /watchdog /watchdog

ENTRYPOINT ["/watchdog"]
