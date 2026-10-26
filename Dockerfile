# syntax=docker/dockerfile:1

# --- Build stage ---
FROM golang:1.23-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./

# Cache module downloads first.
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/spotvee ./cmd/spotvee

# --- Runtime stage ---
FROM alpine:3.20
RUN adduser -D -u 10001 spotvee
COPY --from=build /out/spotvee /usr/local/bin/spotvee
USER spotvee
EXPOSE 8080
# Healthcheck uses busybox wget against the app's /healthz endpoint.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["wget", "-qO-", "http://127.0.0.1:8080/healthz"]
VOLUME ["/data"]
ENV SPOTVEE_TOKEN_FILE=/data/spotvee-state.json \
    SPOTVEE_TOKEN_KEY_FILE=/data/.spotvee-token-key \
    SPOTVEE_LISTEN_ADDR=:8080
ENTRYPOINT ["/usr/local/bin/spotvee"]