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
	node --test .github/actions/qualify-report/live.test.cjs
	bash scripts/test-action-inputs.sh
	python3 scripts/test-release.py
	python3 scripts/test-release-harnesses.py
	python3 scripts/test-qualification-command.py
	python3 scripts/test-qualification-cleanup.py
	python3 scripts/test-qualification-record.py
	python3 scripts/test-qualification-workflows.py
	python3 scripts/test-pilot-cleanup-faults.py
	python3 scripts/test-pilot-restoration.py
	python3 scripts/test-core-qualification.py
	python3 scripts/test-attempt5-gates.py
	python3 scripts/test-worker-heartbeat-observation.py
	python3 scripts/test-pilot-cancellation.py
	python3 scripts/test-pilot-reliability-import.py
	actionlint

external: build
	bash scripts/validate-external.sh "$(CURDIR)/bin/cloudforge"

check: test vet lint vuln actions build
