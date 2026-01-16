# SPDX-License-Identifier: Apache-2.0

# Build the kube-compare-mcp binary using Red Hat UBI Go Toolset
FROM registry.access.redhat.com/ubi9/go-toolset:9.7 AS builder

ARG TARGETOS
ARG TARGETARCH

# UBI go-toolset uses /opt/app-root/src as the default working directory
WORKDIR /opt/app-root/src

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy the go source
COPY cmd/ cmd/
COPY pkg/ pkg/

# Build arguments for version info
ARG VERSION=dev

# Build the binary
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build \
    -ldflags "-X main.version=${VERSION}" \
    -o build/kube-compare-mcp \
    ./cmd/kube-compare-mcp

#####################################################################################################
# Build the runtime image 
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

ENV SUMMARY="MCP server for kube-compare" \
    DESCRIPTION="Model Context Protocol server that enables AI assistants to compare \
Kubernetes cluster configurations against reference templates."

LABEL name="kube-compare-mcp" \
      summary="${SUMMARY}" \
      description="${DESCRIPTION}" \
      io.k8s.display-name="kube-compare-mcp" \
      io.k8s.description="${DESCRIPTION}" \
      io.openshift.tags="kubernetes,mcp,kube-compare,ai"

# Install diff utility which is required by kube-compare
RUN microdnf install -y diffutils --nodocs && \
    microdnf clean all

COPY --from=builder \
    /opt/app-root/src/build/kube-compare-mcp \
    /usr/local/bin/

# Copy the license
COPY LICENSE /licenses/LICENSE

ENV USER_UID=65532

USER ${USER_UID}

# Expose the default port
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/kube-compare-mcp"]
CMD ["--transport", "http", "--port", "8080"]
