DOCS_REPO := https://github.com/RayLight1732/hardcore-together-docs.git

all: fmt vet mod lint

# Run tests (-race required: adapter/memstate has a concurrency test, architecture-manager.md 12/13節)
test: fmt vet
	go test ./... -race

# Refresh docs/ from the hardcore-together-docs repository
.PHONY: docs
docs:
	rm -rf .docs-tmp
	git clone --depth 1 $(DOCS_REPO) .docs-tmp
	rm -rf docs
	cp -r .docs-tmp/docs docs
	rm -rf .docs-tmp

# Run go fmt against code
fmt:
	go fmt ./...

# Run go mod tidy/verify
mod:
	go mod tidy && go mod verify

# Run go vet against code
vet:
	go vet ./...

# Run golangci-lint against code
lint:
	go run github.com/golangci/golangci-lint/cmd/golangci-lint@latest run
