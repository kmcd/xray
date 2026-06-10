#!/bin/sh
# install.sh — install xray (https://github.com/kmcd/xray)
#
# Usage:
#   curl -sSfL https://raw.githubusercontent.com/kmcd/xray/main/scripts/install.sh | sh
#
# Env overrides:
#   XRAY_VERSION        Tag to install (default: resolved from releases/latest)
#   XRAY_INSTALL_DIR    Install location (default: /usr/local/bin)
#   XRAY_VERIFY_COSIGN  Set to 1 to verify the cosign signature on checksums.txt
#                       (requires cosign on PATH; the README "Verifying the binary"
#                       section is the higher-assurance path)

set -eu

REPO=kmcd/xray
INSTALL_DIR=${XRAY_INSTALL_DIR:-/usr/local/bin}

err() {
    printf 'install.sh: %s\n' "$*" >&2
    exit 1
}

require() {
    command -v "$1" >/dev/null 2>&1 || err "missing required tool: $1"
}

detect_os() {
    case "$(uname -s)" in
        Linux)  printf '%s' linux ;;
        Darwin) printf '%s' darwin ;;
        *)      err "unsupported OS: $(uname -s) (Linux and macOS only)" ;;
    esac
}

detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64)  printf '%s' amd64 ;;
        aarch64|arm64) printf '%s' arm64 ;;
        *)             err "unsupported arch: $(uname -m) (amd64 and arm64 only)" ;;
    esac
}

resolve_version() {
    if [ -n "${XRAY_VERSION:-}" ]; then
        printf '%s' "${XRAY_VERSION#v}"
        return
    fi
    if [ -n "${GITHUB_TOKEN:-}" ]; then
        response=$(curl -sSfL --max-time 30 \
            -H "Authorization: Bearer ${GITHUB_TOKEN}" \
            "https://api.github.com/repos/${REPO}/releases/latest") \
            || err "could not fetch latest release info from GitHub API"
    else
        response=$(curl -sSfL --max-time 30 \
            "https://api.github.com/repos/${REPO}/releases/latest") \
            || err "could not fetch latest release info from GitHub API"
    fi
    tag=$(printf '%s' "$response" \
        | grep -o '"tag_name": *"[^"]*"' \
        | head -n 1 \
        | sed -e 's/"tag_name": *"v\{0,1\}//' -e 's/"$//')
    [ -n "$tag" ] || err "could not parse latest release tag from API response"
    printf '%s' "$tag"
}

sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    else
        err "no sha256 verifier on PATH (need sha256sum or shasum)"
    fi
}

verify_sha256() {
    expected=$(awk -v f="$1" '$2==f {print $1}' checksums.txt)
    [ -n "$expected" ] || err "no checksum entry for $1 in checksums.txt"
    actual=$(sha256_of "$1")
    [ "$expected" = "$actual" ] || err "sha256 mismatch for $1: expected $expected, got $actual"
}

verify_cosign() {
    require cosign
    curl -sSfL -O "${BASE_URL}/checksums.txt.sig" \
        || err "could not download cosign signature"
    curl -sSfL -O "${BASE_URL}/checksums.txt.pem" \
        || err "could not download cosign certificate"
    cosign verify-blob \
        --certificate-identity-regexp "https://github.com/${REPO}/.*" \
        --certificate-oidc-issuer https://token.actions.githubusercontent.com \
        --certificate checksums.txt.pem \
        --signature checksums.txt.sig \
        checksums.txt >&2
}

require curl
require tar
require uname
require install

OS=$(detect_os)
ARCH=$(detect_arch)
VERSION=$(resolve_version)
ARCHIVE=xray_${VERSION}_${OS}_${ARCH}.tar.gz
BASE_URL=https://github.com/${REPO}/releases/download/v${VERSION}

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT
cd "$workdir"

printf 'install.sh: downloading xray v%s (%s/%s)\n' "$VERSION" "$OS" "$ARCH" >&2
curl -sSfL -O "${BASE_URL}/${ARCHIVE}"
curl -sSfL -O "${BASE_URL}/checksums.txt"

printf 'install.sh: verifying sha256\n' >&2
verify_sha256 "$ARCHIVE"

if [ "${XRAY_VERIFY_COSIGN:-0}" = "1" ]; then
    printf 'install.sh: verifying cosign signature\n' >&2
    verify_cosign
fi

tar -xzf "$ARCHIVE"
[ -f xray ] || err "archive layout unexpected: no xray binary at root of $ARCHIVE"

mkdir -p "$INSTALL_DIR" || err "could not create install dir $INSTALL_DIR"
if ! install -m 0755 xray "${INSTALL_DIR}/xray"; then
    err "install to ${INSTALL_DIR}/xray failed; re-run with sudo or set XRAY_INSTALL_DIR=\$HOME/.local/bin"
fi

printf 'install.sh: installed xray to %s/xray\n' "$INSTALL_DIR" >&2
"${INSTALL_DIR}/xray" version
