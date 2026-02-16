#!/usr/bin/env bash
# Generate a FAISS vector DB from rag-content/ and package it as a
# dual-platform (amd64 + arm64) container image.
#
# Prerequisites:
#   - The ragtool image must already exist (run scripts/build-ragtool.sh first).
#
# Usage:
#   scripts/build-rag.sh                                 # defaults
#   RAG_IMG=quay.io/me/test:rag scripts/build-rag.sh
#   PUSH=0 scripts/build-rag.sh                          # build only
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

: "${CONTAINER_TOOL:=podman}"
: "${RAGTOOL_IMG:=quay.io/${USER}/kube-compare-mcp:ragtool}"
: "${RAG_IMG:=quay.io/${USER}/kube-compare-mcp:rag}"
: "${PUSH:=1}"

RAG_CONTENT="${REPO_ROOT}/rag-content"
OUTPUT_DIR="${REPO_ROOT}/.rag-build/output"
CONTAINERFILE="${REPO_ROOT}/rag/Containerfile.rag"

info()  { echo "==> $*"; }
error() { echo "ERROR: $*" >&2; exit 1; }

command -v "${CONTAINER_TOOL}" >/dev/null 2>&1 || error "${CONTAINER_TOOL} not found"
[[ -d "${RAG_CONTENT}" ]] || error "rag-content directory not found: ${RAG_CONTENT}"
[[ -f "${CONTAINERFILE}" ]] || error "Containerfile not found: ${CONTAINERFILE}"

# Detect native arch for running the ragtool container
ARCH="$(uname -m)"
case "${ARCH}" in
    x86_64)  NATIVE_PLATFORM="linux/amd64"; RAGTOOL_RUN_TAG="${RAGTOOL_IMG}-amd64" ;;
    arm64|aarch64) NATIVE_PLATFORM="linux/arm64"; RAGTOOL_RUN_TAG="${RAGTOOL_IMG}-arm64" ;;
    *) error "Unsupported architecture: ${ARCH}" ;;
esac

# ---------------------------------------------------------------------------
# 1. Generate vector DB
# ---------------------------------------------------------------------------
info "Generating vector DB (platform: ${NATIVE_PLATFORM})"
rm -rf "${OUTPUT_DIR}"
mkdir -p "${OUTPUT_DIR}"

"${CONTAINER_TOOL}" run --rm \
    --platform "${NATIVE_PLATFORM}" \
    -v "${RAG_CONTENT}:/markdown:Z" \
    -v "${OUTPUT_DIR}:/output:Z" \
    "${RAGTOOL_RUN_TAG}"

[[ -d "${OUTPUT_DIR}/vector_db" ]] || error "vector_db not found in ${OUTPUT_DIR}. Embedding generation failed."

info "Vector DB generated:"
ls -lh "${OUTPUT_DIR}/vector_db/"

# ---------------------------------------------------------------------------
# 2. Build per-platform RAG data images
# ---------------------------------------------------------------------------
AMD64_TAG="${RAG_IMG}-amd64"
ARM64_TAG="${RAG_IMG}-arm64"
BUILD_CTX="${OUTPUT_DIR}"

cp "${CONTAINERFILE}" "${BUILD_CTX}/Containerfile.rag"

info "Building amd64 image: ${AMD64_TAG}"
"${CONTAINER_TOOL}" build \
    --platform linux/amd64 \
    -t "${AMD64_TAG}" \
    -f "${BUILD_CTX}/Containerfile.rag" \
    "${BUILD_CTX}"

info "Building arm64 image: ${ARM64_TAG}"
"${CONTAINER_TOOL}" build \
    --platform linux/arm64 \
    -t "${ARM64_TAG}" \
    -f "${BUILD_CTX}/Containerfile.rag" \
    "${BUILD_CTX}"

# ---------------------------------------------------------------------------
# 3. Assemble multi-arch manifest
# ---------------------------------------------------------------------------
info "Creating manifest: ${RAG_IMG}"
"${CONTAINER_TOOL}" manifest rm "${RAG_IMG}" 2>/dev/null || true
"${CONTAINER_TOOL}" manifest create "${RAG_IMG}" "${AMD64_TAG}" "${ARM64_TAG}"

# ---------------------------------------------------------------------------
# 4. Push
# ---------------------------------------------------------------------------
if [[ "${PUSH}" == "1" ]]; then
    info "Pushing manifest: ${RAG_IMG}"
    "${CONTAINER_TOOL}" manifest push --all "${RAG_IMG}" "docker://${RAG_IMG}"
    info "Done. Pushed ${RAG_IMG}"
else
    info "Skipping push (PUSH=0). Manifest available locally as ${RAG_IMG}"
fi
