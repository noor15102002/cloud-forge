.PHONY: actions build check external fmt test vet lint vuln

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
	bash scripts/test-action-inputs.sh
	actionlint

external: build
	bash scripts/validate-external.sh "$(CURDIR)/bin/cloudforge"

check: test vet lint vuln actions build
