.PHONY: test vet lint check

test:
	go test ./... -count=1

vet:
	go vet ./...

lint:
	golangci-lint run

check: vet test lint
