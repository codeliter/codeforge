# codeforge is two programs: the CLI and codeforge-forms, which asks the questions at the terminal and must sit next to
# it. Install both with `make install`; `go install .` alone leaves every menu and prompt without its helper.
.PHONY: install test lint cover vuln check
install:
	go install . ./cmd/codeforge-forms

test:
	go test -race ./...

lint:
	golangci-lint run ./...
	go vet -tags eval ./...

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# What CI runs on every pull request.
check: lint test vuln
