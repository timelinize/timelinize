
OS_NAME=$(shell go env GOOS)
OS_ARCH=$(shell go env GOARCH)

ZIG_DEP=zig
ZIG_DEP_WHICH=$(shell command -v $(ZIG_DEP))

VIPS_DEP=vips
VIPS_WHICH=$(shell command -v $(VIPS_DEP))

BIN_ROOT=$(PWD)/.bin
BIN_NAME=timeline
BIN_NAME_NATIVE=$(BIN_NAME)_$(OS_NAME)_$(OS_ARCH)
ifeq ($(OS_NAME),windows)
	BIN_NAME_NATIVE=$(BIN_NAME)_$(OS_NAME)_$(OS_ARCH).exe
endif

export PATH:=$(BIN_ROOT):$(PATH)

all: bin

### dep

dep:
ifeq ($(ZIG_DEP_WHICH), )
	@echo ""
	@echo "$(ZIG_DEP) dep check: failed"
	$(MAKE) dep-zig
endif

ifeq ($(VIPS_WHICH), )
	@echo ""
	@echo "$(VIPS_DEP) dep check: failed"
	$(MAKE) dep-libvps
endif
dep-del:
	brew uninstall zig
	brew uninstall libvips
	brew autoremove
dep-zig:
	# https://github.com/ziglang/zig/releases/tag/0.13.0
	brew install zig
dep-libvps:
	# https://github.com/libvips/libvips/releases/tag/v8.15.3
	brew install libvips

### bin

bin: dep
	# darwin amd64
	#CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -o $(BIN_ROOT)/$(BIN_NAME)_darwin_amd64
	# darwin arm64
	go build -o $(BIN_ROOT)/$(BIN_NAME)_darwin_arm64
bin-cross:
	# linux amd64
	#CGO_ENABLED=1 GOOS=linux GOARCH=amd64 CC="zig cc -target x86_64-linux" CXX="zig c++ -target x86_64-linux" go build -o $(BIN_ROOT)/$(BIN_NAME)_linux_amd64
	
	# linux arm64
	CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC="zig cc -target aarch64-linux" CXX="zig c++ -target aarch64-linux" go build -o $(BIN_ROOT)/$(BIN_NAME)_linux_arm64

	# windows amd64
	CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC="zig cc -target x86_64-windows" CXX="zig c++ -target x86_64-windows" go build -o $(BIN_ROOT)/$(BIN_NAME)_windows_amd64.exe

	# windows arm64
	CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC="zig cc -target x86_64-windows" CXX="zig c++ -target x86_64-windows" go build -o $(BIN_ROOT)/$(BIN_NAME)_windows_arm64.exe

	
	
### run 

run-h:
	$(BIN_NAME_NATIVE) -h

run:
	$(BIN_NAME_NATIVE)
