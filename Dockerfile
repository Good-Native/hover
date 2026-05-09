# Alloy metrics agent
FROM grafana/alloy:v1.15.1@sha256:1f40cf52adda8fab3e058f9347a5d165624ecb9fbc1527769cb744748961940d AS alloy

# Build stage
FROM golang:1.26.3-alpine AS builder

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build both binaries in a single layer
RUN CGO_ENABLED=0 GOOS=linux go build -o main ./cmd/app/main.go && \
    CGO_ENABLED=0 GOOS=linux go build -o worker ./cmd/worker/main.go

# Final stage
FROM alpine:3.19

# Create a non-root user
RUN adduser -D -g '' appuser

WORKDIR /app

# Install ca-certificates for HTTPS; gcompat provides glibc compat for Alloy sidecar
RUN apk --no-cache add \
  ca-certificates=20250911-r0 \
  gcompat=1.1.0-r4

# Copy Go binaries (API + worker)
COPY --from=builder /app/main .
COPY --from=builder /app/worker .

# Copy Alloy binary and config
COPY --from=alloy /bin/alloy /usr/local/bin/alloy
COPY alloy.river .

# Copy startup script
COPY scripts/start.sh .
RUN chmod +x start.sh /usr/local/bin/alloy

# Copy static files
COPY --from=builder /app/dashboard.html .
COPY --from=builder /app/homepage.html .
COPY --from=builder /app/settings.html .
COPY --from=builder /app/welcome.html .
COPY --from=builder /app/invite-welcome.html .
COPY --from=builder /app/auth-modal.html .
COPY --from=builder /app/auth-callback.html .
COPY --from=builder /app/web/static ./web/static
COPY --from=builder /app/web/partials ./web/partials
COPY --from=builder /app/web/templates ./web/templates

# Copy migration files (required for reset-db endpoint)
COPY --from=builder /app/supabase/migrations ./supabase/migrations

# Add healthcheck
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

# Expose port
EXPOSE 8080

# Switch to non-root user
USER appuser

# Run app + Alloy sidecar
CMD ["./start.sh"]
