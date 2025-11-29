# Multi-stage build for smaller image
FROM golang:1.22-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git

WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the main settlement binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o settlement cmd/main.go

# Build the worker binary (required by supervisor)
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o worker cmd/worker/main.go

# Final stage - minimal runtime image
FROM alpine:latest

# Install ca-certificates for HTTPS
RUN apk --no-cache add ca-certificates

WORKDIR /root/

# Copy both binaries from builder
COPY --from=builder /app/settlement .
COPY --from=builder /app/worker .

# Copy .env file if it exists (for local development)
COPY .env* ./

# Expose API port
EXPOSE 8080

# Expose worker gRPC ports (50051-50065 for up to 15 workers)
EXPOSE 50051-50065

# Run the settlement server
CMD ["./settlement"]
