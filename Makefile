.PHONY: actions build check fmt test vet lint vuln

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

actions:
	node --test .github/actions/report/comment.test.cjs
	actionlint

check: test vet lint vuln actions build
