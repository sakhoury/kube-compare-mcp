#!/usr/bin/env bash
# Build and push a dual-platform (amd64 + arm64) ragtool image.
#
# The ragtool image contains the Python embedding pipeline from
# openshift/lightspeed-rag-content. It accepts markdown files at /markdown
# and writes a FAISS vector DB to /output.
#
# Usage:
#   scripts/build-ragtool.sh                          # defaults
#   RAGTOOL_IMG=quay.io/me/test:ragtool scripts/build-ragtool.sh
#   PUSH=0 scripts/build-ragtool.sh                   # build only
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

: "${CONTAINER_TOOL:=podman}"
: "${RAGTOOL_IMG:=quay.io/${USER}/kube-compare-mcp:ragtool}"
: "${UPSTREAM_REPO:=https://github.com/openshift/lightspeed-rag-content.git}"
: "${UPSTREAM_REF:=main}"
: "${PUSH:=1}"

WORK_DIR="${REPO_ROOT}/.rag-build"
UPSTREAM_DIR="${WORK_DIR}/lightspeed-rag-content"
CONTAINERFILE="${REPO_ROOT}/rag/Containerfile.ragtool"

info()  { echo "==> $*"; }
error() { echo "ERROR: $*" >&2; exit 1; }

command -v "${CONTAINER_TOOL}" >/dev/null 2>&1 || error "${CONTAINER_TOOL} not found"
[[ -f "${CONTAINERFILE}" ]] || error "Containerfile not found: ${CONTAINERFILE}"

# ---------------------------------------------------------------------------
# 1. Clone / update upstream repo (pyproject.toml, embeddings_model, etc.)
# ---------------------------------------------------------------------------
info "Preparing upstream content in ${UPSTREAM_DIR}"
if [[ -d "${UPSTREAM_DIR}/.git" ]]; then
    git -C "${UPSTREAM_DIR}" fetch origin "${UPSTREAM_REF}"
    git -C "${UPSTREAM_DIR}" checkout FETCH_HEAD
else
    mkdir -p "${WORK_DIR}"
    git clone --depth 1 --branch "${UPSTREAM_REF}" "${UPSTREAM_REPO}" "${UPSTREAM_DIR}"
fi

# Copy our Containerfile into the build context
cp "${CONTAINERFILE}" "${UPSTREAM_DIR}/Containerfile.ragtool"

# ---------------------------------------------------------------------------
# 2. Build per-platform images
#    amd64 -> --group cpu  (x86_64-pinned torch wheel, smaller)
#    arm64 -> --group gpu  (generic torch==2.6.0, resolves per-platform)
# ---------------------------------------------------------------------------
AMD64_TAG="${RAGTOOL_IMG}-amd64"
ARM64_TAG="${RAGTOOL_IMG}-arm64"

info "Building amd64 image: ${AMD64_TAG}"
"${CONTAINER_TOOL}" build \
    --platform linux/amd64 \
    --build-arg PDM_GROUP=cpu \
    -t "${AMD64_TAG}" \
    -f "${UPSTREAM_DIR}/Containerfile.ragtool" \
    "${UPSTREAM_DIR}"

info "Building arm64 image: ${ARM64_TAG}"
"${CONTAINER_TOOL}" build \
    --platform linux/arm64 \
    --build-arg PDM_GROUP=gpu \
    -t "${ARM64_TAG}" \
    -f "${UPSTREAM_DIR}/Containerfile.ragtool" \
    "${UPSTREAM_DIR}"

# ---------------------------------------------------------------------------
# 3. Assemble multi-arch manifest
# ---------------------------------------------------------------------------
info "Creating manifest: ${RAGTOOL_IMG}"
"${CONTAINER_TOOL}" manifest rm "${RAGTOOL_IMG}" 2>/dev/null || true
"${CONTAINER_TOOL}" manifest create "${RAGTOOL_IMG}" "${AMD64_TAG}" "${ARM64_TAG}"

# ---------------------------------------------------------------------------
# 4. Push
# ---------------------------------------------------------------------------
if [[ "${PUSH}" == "1" ]]; then
    info "Pushing manifest: ${RAGTOOL_IMG}"
    "${CONTAINER_TOOL}" manifest push --all "${RAGTOOL_IMG}" "docker://${RAGTOOL_IMG}"
    info "Done. Pushed ${RAGTOOL_IMG}"
else
    info "Skipping push (PUSH=0). Manifest available locally as ${RAGTOOL_IMG}"
fi
