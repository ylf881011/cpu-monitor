GO              ?=  go
BIN_DIR         ?= $(shell pwd)/bin
GIT_BRANCH      ?= `git symbolic-ref --short -q HEAD`
GIT_COMMIT      ?= `git rev-parse --short HEAD`
BUILD_DATE      ?= `date +%FT%T%z`
V:=$(shell cat VERSION)
deploy: clean linux
CC ?= "gcc"

# Get OS architecture
OS=$(shell uname)
ifeq ($(OS),Linux)
OS=linux
else ifeq ($(OS),Darwin)
OS=darwin
endif

OSARCH=$(shell uname -m)
ifeq ($(OSARCH),x86_64)
OSARCH=amd64
else ifeq ($(OSARCH),x64)
OSARCH=amd64
else ifeq ($(OSARCH),aarch64)
OSARCH=arm64
else ifeq ($(OSARCH),aarch64_be)
OSARCH=arm64
else ifeq ($(OSARCH),armv8b)
OSARCH=arm64
else ifeq ($(OSARCH),armv8l)
OSARCH=arm64
else ifeq ($(OSARCH),i386)
OSARCH=x86
else ifeq ($(OSARCH),i686)
OSARCH=x86
else ifeq ($(OSARCH),arm)
OSARCH=arm
endif

REL_OSARCH=${OS}/${OSARCH}

gox:
	go get github.com/mitchellh/gox
	go mod vendor && go mod tidy

linux: clean
	CC=${CC} CGO_ENABLED=0 gox -osarch=linux/amd64 -ldflags=${LD_FLAGS} -output="cpu-monitor" .

macos: clean
	GOARCH=amd64 GOOS=darwin go build -o cpu-monitor .

clean:
	rm -rf cpu-monitor