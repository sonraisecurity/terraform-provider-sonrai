BINARY := terraform-provider-sonrai

# The toolchain every target builds and scans with, matching the CircleCI
# executor so a local `make check` sees what CI sees. go.mod deliberately does
# not pin this: its go directive is a hard minimum, so raising it to a patch
# release stops any consumer on an older Go from reading the module at all --
# dependency scanners included, and those run only when go.mod changes, so the
# breakage surfaces long after the commit that caused it.
export GOTOOLCHAIN := go1.26.11

# Build tools are pinned and fetched with `go run`, which verifies them against
# the Go module checksum database. The release job imports the registry signing
# key, so an unverified curl'd binary there could sign artifacts as Sonrai.
#
# GoReleaser needs a newer Go than the provider builds with, so it is installed
# (still checksum-verified) with its own toolchain and then run under the one
# above -- the provider binaries must come from GOTOOLCHAIN, not from this.
#
# Pinned to a commit, not a tag, so a moved tag cannot change what runs here.
# This is the commit tagged v2.19.0; update both together.
GORELEASER_COMMIT := 00139d15712716a3a2f21eb4d533f231ff19b9c5
GORELEASER_TOOLCHAIN := go1.27.3
GORELEASER_BIN := $(CURDIR)/.tools/goreleaser-$(GORELEASER_COMMIT)
GORELEASER := $(GORELEASER_BIN)/goreleaser
TFPLUGINDOCS := go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.25.0

.PHONY: mod build test vet fmt fmt-check check ci-check install docs docs-check release-check release-artifacts vulncheck

mod:
	go mod download

build:
	go build -o $(BINARY) .

test:
	go test ./... -race

vet:
	go vet ./...

fmt:
	gofmt -l -w .

check: fmt vet test

# Fails, rather than rewrites, when files are not gofmt'd.
fmt-check:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "These files are not gofmt'd:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

# What CI's test job runs.
ci-check: fmt-check vet test docs-check vulncheck

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

$(GORELEASER):
	GOTOOLCHAIN=$(GORELEASER_TOOLCHAIN) GOBIN=$(GORELEASER_BIN) go install github.com/goreleaser/goreleaser/v2@$(GORELEASER_COMMIT)

# Builds the release artifacts without publishing or signing them.
release-check: $(GORELEASER)
	$(GORELEASER) release --snapshot --clean --skip=sign

# Builds and signs the release artifacts into dist/ without publishing. Run by
# .circleci/release.sh with HEAD tagged locally; toolkit/release publishes.
release-artifacts: $(GORELEASER)
	$(GORELEASER) release --clean --skip=publish,announce

# Reports known vulnerabilities in the dependencies and the toolchain that this
# code actually reaches. Run it before committing a dependency change.
vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
