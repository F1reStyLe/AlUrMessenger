# syntax=docker/dockerfile:1
FROM golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

# No shell/package manager; runtime contains CA certificates and runs as uid 65532.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS runtime
COPY --from=build /out/ /app/
USER 65532:65532
FROM runtime AS api
ENTRYPOINT ["/app/api"]
FROM runtime AS worker
ENTRYPOINT ["/app/worker"]
FROM runtime AS migrate
ENTRYPOINT ["/app/migrate"]
FROM runtime AS minio-init
ENTRYPOINT ["/app/minio-init"]
