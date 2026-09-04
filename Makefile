.PHONY: revtether test

revtether:
	mise exec -- go build -o bin/revtether ./cmd/revtether

test:
	mise exec -- go test ./...
