BINARY := terraform-provider-sonrai

# The toolchain every target builds and scans with. go.mod deliberately does
# not pin this: its go directive is a hard minimum, so raising it to a patch
# release stops any consumer on an older Go from reading the module at all --
# dependency scanners included, and those run only when go.mod changes, so the
# breakage surfaces long after the commit that caused it.
export GOTOOLCHAIN := go1.26.11

# Build tools are pinned and fetched with `go run`, which verifies them against
# the Go module checksum database.
TFPLUGINDOCS := go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.25.0

.PHONY: build test vet fmt check install docs docs-check

build:
	go build -o $(BINARY) .

test:
	go test ./... -race

vet:
	go vet ./...

fmt:
	gofmt -l -w .

check: fmt vet test

# Installs into the Go bin directory for use with a dev_overrides block.
install:
	go install .

# Regenerates docs/ for the Terraform Registry from the provider schemas.
docs:
	$(TFPLUGINDOCS) generate --provider-name sonrai

# Fails when docs/ is behind the schemas.
docs-check: docs
	@if [ -n "$$(git status --porcelain docs/)" ]; then \
		echo "docs/ is out of date -- run 'make docs' and commit the result"; \
		git status --porcelain docs/; \
		exit 1; \
	fi

# Reports known vulnerabilities in the dependencies and the toolchain that this
# code actually reaches. Run it before committing a dependency change.
vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
