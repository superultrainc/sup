.PHONY: dev build test fmt install clean

dev:
	go run . --demo

build:
	go build -o sup .

test:
	go test ./...

fmt:
	gofmt -w .

# Matches install.sh's default location, which is what ends up on PATH.
INSTALL_DIR ?= $(HOME)/.local/bin

install:
	mkdir -p $(INSTALL_DIR)
	go build -o $(INSTALL_DIR)/sup .

clean:
	rm -f sup
