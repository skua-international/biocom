# syntax=docker/dockerfile:1

# ============================================================
# Build stage
# ============================================================
FROM rust:1.77-alpine AS builder

# Install build dependencies
RUN apk add --no-cache musl-dev ca-certificates tzdata

WORKDIR /src

# Copy manifests first for better layer caching
COPY Cargo.toml Cargo.lock* ./

# Create dummy src to cache dependencies
RUN mkdir src && echo "fn main() {}" > src/main.rs

# Build dependencies only (this layer will be cached)
RUN cargo build --release 2>/dev/null || true

# Remove dummy and copy real source
RUN rm -rf src
COPY src ./src

# Build release binary
RUN cargo build --release --locked 2>/dev/null || cargo build --release

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
COPY --from=builder /src/target/release/biocom /biocom

ENTRYPOINT ["/biocom"]
