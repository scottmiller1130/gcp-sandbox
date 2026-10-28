# syntax=docker/dockerfile:1

# BuildKit fills these in automatically; the defaults only matter for the
# legacy builder. Cloud Run runs linux/amd64.
ARG BUILDPLATFORM=linux/amd64

# Build on the runner's native platform and cross-compile, so building an
# amd64 image on an arm64 Mac doesn't need emulation.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/server .

# Distroless: no shell, no package manager, runs as an unprivileged user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/server"]
