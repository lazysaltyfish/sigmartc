.DEFAULT_GOAL := build

.PHONY: build run clean

build:
	go build -o bin/sigmartc cmd/server/main.go

run: build
	./bin/sigmartc

clean:
	rm -rf bin/ server.log
