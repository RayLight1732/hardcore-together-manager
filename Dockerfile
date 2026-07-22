FROM --platform=$BUILDPLATFORM golang:1.25 AS build

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.sum ./

# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/manager ./cmd/manager
COPY internal ./internal

# Automatically provided by the buildkit
ARG TARGETOS TARGETARCH

# Build (cmd/fakehardcore is a test-only stub and is intentionally excluded
# from the product image, architecture-manager.md 1節)
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -ldflags="-s -w" -a -o manager ./cmd/manager

# Manager launches the hardcore server as an os/exec child process
# ("java -jar server.jar", architecture-manager.md 9節/11節), so the
# runtime image needs a JRE alongside the Manager binary. Minecraft
# 1.21.1 (hardcore-together-neoforge) requires Java 21.
FROM eclipse-temurin:21-jre-jammy AS app
WORKDIR /app
COPY --from=build /workspace/manager /app/manager
CMD ["/app/manager"]
