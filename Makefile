# ---- Config ----
BINARY      := gps
PKG         := .
BUILD_DIR   := dist
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)

# Target platforms: OS/ARCH pairs
PLATFORMS := \
	linux/amd64 \
	linux/arm64 \
	darwin/amd64 \
	darwin/arm64 \
	windows/amd64

.PHONY: all build clean run cross test vet $(PLATFORMS)

# Build for your current machine
build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

run: build
	./$(BINARY)

test:
	go test ./...

vet:
	go vet ./...

# Build for every platform in PLATFORMS
all: cross
cross: $(PLATFORMS)

# Pattern rule: each "os/arch" target builds one binary
$(PLATFORMS):
	$(eval GOOS := $(word 1,$(subst /, ,$@)))
	$(eval GOARCH := $(word 2,$(subst /, ,$@)))
	$(eval EXT := $(if $(filter windows,$(GOOS)),.exe,))
	@echo ">> building $(GOOS)/$(GOARCH)"
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) \
		go build -ldflags "$(LDFLAGS)" \
		-o $(BUILD_DIR)/$(BINARY)-$(GOOS)-$(GOARCH)$(EXT) $(PKG)

# Convenience shortcut for Ubuntu/Linux amd64
.PHONY: ubuntu
ubuntu: linux/amd64

clean:
	rm -rf $(BUILD_DIR) $(BINARY)
