# Build Stage
FROM golang:1.24.2-alpine AS builder
MAINTAINER github.com/xquai

WORKDIR /app

# Kopieren der Go Module Dateien
COPY go.mod go.sum ./
RUN go mod download

# Kopieren des Quellcodes
COPY . .

# Bauen der Anwendung
RUN CGO_ENABLED=0 GOOS=linux go build -o main .

# Final Stage
FROM alpine:latest
MAINTAINER github.com/xquai

WORKDIR /app

# Kopieren der ausführbaren Datei aus dem Build Stage
COPY --from=builder /app/main .

# Standardmäßiger Konfigurationspfad
ENV CONFIG_PATH=/app/config/config.yaml

# Erstellen des Konfigurationsverzeichnisses
RUN mkdir -p /app/config

# Nicht-Root Benutzer für mehr Sicherheit
RUN adduser -D appuser
USER appuser

CMD ["./main"]