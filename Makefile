.PHONY: actions build check external fmt test vet lint vuln release

RELEASE_VERSION ?= v0.1.0-alpha.1
RELEASE_OUTPUT ?= $(CURDIR)/dist

build:
	go build -o bin/cloudforge ./cmd/cloudforge

release:
	bash scripts/build-release.sh "$(RELEASE_VERSION)" "$(RELEASE_OUTPUT)"

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
	python3 scripts/test-release.py
	python3 scripts/test-release-harnesses.py
	python3 scripts/test-qualification-command.py
	actionlint

external: build
	bash scripts/validate-external.sh "$(CURDIR)/bin/cloudforge"

check: test vet lint vuln actions build
