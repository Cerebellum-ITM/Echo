BINARY_NAME = echo_cli
CMD_PATH    = .
VERSION_PKG = github.com/pascualchavez/echo/internal/repl
INSTALL_DIR = $(HOME)/.local/bin

# Build metadata: always `+<shortsha>`, plus a `.dirty` marker when the
# working tree has uncommitted or untracked changes. The commit is shown
# even on a clean build so you can always tell exactly which revision a
# moved binary was built from — a bare semver alone can't disambiguate
# two builds made between releases.
GIT_SHA   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
GIT_DIRTY := $(shell test -z "$$(git status --porcelain 2>/dev/null)" || echo .dirty)
VERSION_META := +$(GIT_SHA)$(GIT_DIRTY)
LDFLAGS := -ldflags "-X '$(VERSION_PKG).VersionMeta=$(VERSION_META)'"

.PHONY: build build_release clean

build:
	@echo "Building and installing binary (meta: $(if $(VERSION_META),$(VERSION_META),clean))..."
	@mkdir -p $(INSTALL_DIR)
	@go build -trimpath $(LDFLAGS) -o $(INSTALL_DIR)/$(BINARY_NAME) $(CMD_PATH)
	@echo "Installed at $(INSTALL_DIR)/$(BINARY_NAME)"

install_local:
	@echo "Installed at $(INSTALL_DIR)/$(BINARY_NAME)"
	@mv ./bin/$(BINARY_NAME)_darwin_arm64 $(INSTALL_DIR)/$(BINARY_NAME)

build_release:
	@echo "Building release binaries (meta: $(if $(VERSION_META),$(VERSION_META),clean))..."
	@rm -rf ./bin
	@mkdir -p ./bin
	@go build -trimpath $(LDFLAGS) -o ./bin/$(BINARY_NAME)_darwin_arm64 $(CMD_PATH)
	@GOOS=linux GOARCH=amd64 go build -trimpath $(LDFLAGS) -o ./bin/$(BINARY_NAME)_linux_amd64 $(CMD_PATH)
	@GOOS=linux GOARCH=arm64 go build -trimpath $(LDFLAGS) -o ./bin/$(BINARY_NAME)_linux_arm64 $(CMD_PATH)
	@echo "Release binaries created in ./bin/"
## ship: [interactive]
ship:
	teleport ship

clean:
	@rm -rf ./bin
