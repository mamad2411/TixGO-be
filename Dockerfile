# TixGo backend — multi-stage, non-root numerik, healthcheck (docs/13 §3)
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/api ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -o /out/worker ./cmd/worker

FROM alpine:3.20
# Versi paket alpine bergeser per indeks; pin versi memecah build -> sengaja tidak di-pin.
# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates wget \
 && adduser -D -u 10001 app
USER 10001
COPY --from=build /out/ /app/
EXPOSE 8081
HEALTHCHECK --interval=15s --timeout=3s CMD ["wget", "-qO-", "http://127.0.0.1:8081/healthz"]
ENTRYPOINT ["/app/api"]
