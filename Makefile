# codeforge is two programs: the CLI and codeforge-forms, which asks the questions at the terminal and must sit next to
# it. Install both with `make install`; `go install .` alone leaves every menu and prompt without its helper.
.PHONY: install
install:
	go install . ./cmd/codeforge-forms
