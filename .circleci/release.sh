#!/usr/bin/env bash
# Build command for toolkit/release. Produces the Terraform provider release
# artifacts in dist/release/, which toolkit/release attaches to the GitHub
# release it creates after the commitizen bump.
#
# Inputs (exported by the toolkit's detect_version step):
#   VERSION, TAG_VERSION, SNAPSHOT
# Production also needs (terraform-registry-signing context):
#   GPG_PRIVATE_KEY (base64 of the armored secret key), GPG_FINGERPRINT, PASSPHRASE
set -euo pipefail

: "${VERSION:?VERSION not set}"
: "${TAG_VERSION:?TAG_VERSION not set}"
: "${SNAPSHOT:?SNAPSHOT not set}"

if [[ "$SNAPSHOT" == "true" ]]; then
  echo "Snapshot build: unsigned, nothing published."
  make release-check
  exit 0
fi

# Fail closed on anything that is not a plain semver release.
if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ || "$TAG_VERSION" != "v${VERSION}" ]]; then
  echo "Refusing to release: VERSION='$VERSION' TAG_VERSION='$TAG_VERSION' is not vMAJOR.MINOR.PATCH" >&2
  exit 1
fi

for var in GPG_PRIVATE_KEY GPG_FINGERPRINT PASSPHRASE; do
  if [[ -z "${!var:-}" ]]; then
    echo "Refusing to release: $var is not set (terraform-registry-signing context)" >&2
    exit 1
  fi
done

# Throwaway keyring, removed on exit so the key never outlives this step.
GNUPGHOME="$(mktemp -d)"
chmod 700 "$GNUPGHOME"
export GNUPGHOME

LOCAL_TAG_CREATED=false
cleanup() {
  # The tag below exists only so GoReleaser can validate the version against
  # HEAD. It must be gone before commitizen creates and pushes the real one.
  if [[ "$LOCAL_TAG_CREATED" == "true" ]]; then
    git tag -d "$TAG_VERSION" >/dev/null || true
  fi
  gpgconf --kill gpg-agent 2>/dev/null || true
  rm -rf "$GNUPGHOME"
}
trap cleanup EXIT

printf '%s' "$GPG_PRIVATE_KEY" | base64 -d | gpg --batch --quiet --import
if ! gpg --batch --list-secret-keys "$GPG_FINGERPRINT" >/dev/null 2>&1; then
  echo "Refusing to release: imported key does not match GPG_FINGERPRINT" >&2
  exit 1
fi

# Fails if the tag already exists, which is the right outcome.
git tag "$TAG_VERSION"
LOCAL_TAG_CREATED=true

make release-artifacts

PROJECT=terraform-provider-sonrai
STAGE=dist/release
SUMS="${PROJECT}_${VERSION}_SHA256SUMS"
mkdir -p "$STAGE"
cp dist/"${PROJECT}_${VERSION}"_*.zip "$STAGE"/
cp "dist/${SUMS}" "dist/${SUMS}.sig" "$STAGE"/
# SHA256SUMS already lists the manifest under this name (.goreleaser.yml
# checksum.extra_files); the registry needs the file itself attached too.
cp terraform-registry-manifest.json "$STAGE/${PROJECT}_${VERSION}_manifest.json"

# Verify what will be published before handing it to the toolkit.
gpg --batch --verify "$STAGE/${SUMS}.sig" "$STAGE/${SUMS}"
(cd "$STAGE" && sha256sum --check --strict "$SUMS")

ls -l "$STAGE"
