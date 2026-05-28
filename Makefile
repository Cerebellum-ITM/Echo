BINARY_NAME = echo_cli
CMD_PATH    = .
VERSION_PKG = github.com/pascualchavez/echo/internal/repl

# Build metadata: empty on a clean tree, `+<shortsha>.dirty` when there
# are uncommitted or untracked changes. Lets `echo version` make it
# obvious that the running binary is ahead of the last release commit
# (version bumps land with their release commit, so any dirty build is
# by definition between releases).
GIT_SHA   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
GIT_DIRTY := $(shell test -z "$$(git status --porcelain 2>/dev/null)" || echo dirty)
ifeq ($(GIT_DIRTY),dirty)
VERSION_META := +$(GIT_SHA).dirty
else
VERSION_META :=
endif
LDFLAGS := -ldflags "-X '$(VERSION_PKG).VersionMeta=$(VERSION_META)'"

.PHONY: build build_release clean

build:
	@echo "Building binary (meta: $(if $(VERSION_META),$(VERSION_META),clean))..."
	@mkdir -p ./bin
	@go build -trimpath $(LDFLAGS) -o ./bin/$(BINARY_NAME) $(CMD_PATH)
	@echo "Binary created at ./bin/$(BINARY_NAME)"

build_release:
	@echo "Building release binaries (meta: $(if $(VERSION_META),$(VERSION_META),clean))..."
	@rm -rf ./bin
	@mkdir -p ./bin
	@go build -trimpath $(LDFLAGS) -o ./bin/$(BINARY_NAME)_darwin_arm64 $(CMD_PATH)
	@GOOS=linux GOARCH=amd64 go build -trimpath $(LDFLAGS) -o ./bin/$(BINARY_NAME)_linux_amd64 $(CMD_PATH)
	@GOOS=linux GOARCH=arm64 go build -trimpath $(LDFLAGS) -o ./bin/$(BINARY_NAME)_linux_arm64 $(CMD_PATH)
	@echo "Release binaries created in ./bin/"

clean:
	@rm -rf ./bin
