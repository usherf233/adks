# adks — 常用开发命令。
# Windows 上默认输出 adks.exe；其它平台输出 adks。

BIN := adks
ifeq ($(OS),Windows_NT)
	BIN := adks.exe
endif

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: help build run check test vet fmt lint clean release

help:
	@echo "build    构建 $(BIN)（embed 会在此时快照 web/ kit/ manifest/）"
	@echo "run      构建并启动，默认 http://127.0.0.1:7788"
	@echo "check    build + vet + test -race"
	@echo "test     go test -race -count=1 ./..."
	@echo "fmt      gofmt -l -w ."
	@echo "clean    删除本地产物"

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/adks

run: build
	./$(BIN) serve

check: build
	go vet ./...
	go test -race -count=1 ./...

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

lint:
	go vet ./...

clean:
	rm -f adks adks.exe

# 交叉编译到 dist/，发布用。
release:
	mkdir -p dist
	GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/adks-linux-amd64       ./cmd/adks
	GOOS=linux   GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/adks-linux-arm64       ./cmd/adks
	GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/adks-darwin-arm64      ./cmd/adks
	GOOS=darwin  GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/adks-darwin-amd64      ./cmd/adks
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/adks-windows-amd64.exe ./cmd/adks
