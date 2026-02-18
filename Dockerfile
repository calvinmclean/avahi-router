# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY main.go ./

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o avahi-router .

# Runtime stage
FROM alpine:latest

# Install avahi-utils for avahi-publish command
RUN apk add --no-cache avahi avahi-tools

WORKDIR /app

# Copy the binary from builder
COPY --from=builder /app/avahi-router .

# Run the application
ENTRYPOINT ["./avahi-router"]
