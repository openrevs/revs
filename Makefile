.DEFAULT_GOAL := build

build:
	go build -o bin/revs ./cmd/revs

generate:
	sqlc generate

test:
	go test ./... -count=1

clean:
	rm -rf bin/
