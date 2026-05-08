BINARY_NAME = echo_cli
CMD_PATH    = .

.PHONY: build build_release clean

build:
	@echo "Building binary..."
	@mkdir -p ./bin
	@go build -trimpath -o ./bin/$(BINARY_NAME) $(CMD_PATH)
	@echo "Binary created at ./bin/$(BINARY_NAME)"

build_release:
	@echo "Building release binaries..."
	@rm -rf ./bin
	@mkdir -p ./bin
	@go build -trimpath -o ./bin/$(BINARY_NAME)_darwin_arm64 $(CMD_PATH)
	@GOOS=linux GOARCH=amd64 go build -trimpath -o ./bin/$(BINARY_NAME)_linux_amd64 $(CMD_PATH)
	@GOOS=linux GOARCH=arm64 go build -trimpath -o ./bin/$(BINARY_NAME)_linux_arm64 $(CMD_PATH)
	@echo "Release binaries created in ./bin/"

clean:
	@rm -rf ./bin
