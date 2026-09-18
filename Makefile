.PHONY: build check fmt test vet lint vuln

build:
	go build -o bin/cloudforge ./cmd/cloudforge

fmt:
	gofmt -w cmd internal pkg

test:
	go test -race ./...

vet:
	go vet ./...

lint:
	golangci-lint run

vuln:
	govulncheck ./...

check: test vet lint vuln build
