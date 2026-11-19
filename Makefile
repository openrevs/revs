.DEFAULT_GOAL := build

build:
	go build -o bin/revs ./cmd/revs
	go build -o bin/revs-txt2csv ./cmd/revs-txt2csv

generate:
	sqlc generate

test:
	go test ./... -count=1

clean:
	rm -rf bin/
