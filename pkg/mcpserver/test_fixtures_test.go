// SPDX-License-Identifier: Apache-2.0

package mcpserver_test

import (
	"encoding/base64"
	"io"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// Kubeconfig fixtures for testing
const (
	// ValidKubeconfig is a minimal valid kubeconfig with token auth.
	ValidKubeconfig = `
apiVersion: v1
kind: Config
current-context: test-context
clusters:
- name: test-cluster
  cluster:
    server: https://192.168.1.100:6443
    certificate-authority-data: dGVzdC1jYS1kYXRh
users:
- name: test-user
  user:
    token: test-token-12345
contexts:
- name: test-context
  context:
    cluster: test-cluster
    user: test-user
`

	// ExecAuthKubeconfig contains exec-based auth (should be blocked).
	ExecAuthKubeconfig = `
apiVersion: v1
kind: Config
current-context: exec-context
clusters:
- name: exec-cluster
  cluster:
    server: https://192.168.1.100:6443
    certificate-authority-data: dGVzdC1jYS1kYXRh
users:
- name: exec-user
  user:
    exec:
      command: /usr/local/bin/aws
      args:
        - eks
        - get-token
        - --cluster-name
        - my-cluster
      apiVersion: client.authentication.k8s.io/v1beta1
contexts:
- name: exec-context
  context:
    cluster: exec-cluster
    user: exec-user
`

	// AuthProviderKubeconfig contains auth provider plugin (should be blocked).
	AuthProviderKubeconfig = `
apiVersion: v1
kind: Config
current-context: gcp-context
clusters:
- name: gcp-cluster
  cluster:
    server: https://192.168.1.100:6443
    certificate-authority-data: dGVzdC1jYS1kYXRh
users:
- name: gcp-user
  user:
    auth-provider:
      name: gcp
      config:
        cmd-path: /usr/local/bin/gcloud
contexts:
- name: gcp-context
  context:
    cluster: gcp-cluster
    user: gcp-user
`

	// CertAuthKubeconfig uses client certificate authentication (allowed).
	CertAuthKubeconfig = `
apiVersion: v1
kind: Config
current-context: cert-context
clusters:
- name: cert-cluster
  cluster:
    server: https://192.168.1.100:6443
    certificate-authority-data: dGVzdC1jYS1kYXRh
users:
- name: cert-user
  user:
    client-certificate-data: dGVzdC1jbGllbnQtY2VydA==
    client-key-data: dGVzdC1jbGllbnQta2V5
contexts:
- name: cert-context
  context:
    cluster: cert-cluster
    user: cert-user
`

	// NoCurrentContextKubeconfig has no current-context set.
	NoCurrentContextKubeconfig = `
apiVersion: v1
kind: Config
clusters:
- name: test-cluster
  cluster:
    server: https://localhost:6443
users:
- name: test-user
  user:
    token: test-token
contexts:
- name: test-context
  context:
    cluster: test-cluster
    user: test-user
`
)

// EncodeKubeconfig base64-encodes a kubeconfig string.
func EncodeKubeconfig(kc string) string {
	return base64.StdEncoding.EncodeToString([]byte(kc))
}

// NewMCPRequest creates an MCP CallToolRequest with the given arguments.
func NewMCPRequest(args map[string]interface{}) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: args,
		},
	}
}

// NewMCPRequestWithName creates an MCP CallToolRequest with tool name and arguments.
func NewMCPRequestWithName(name string, args map[string]interface{}) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      name,
			Arguments: args,
		},
	}
}

// NewHTTPResponse creates a simple HTTP response for testing.
func NewHTTPResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Status:     http.StatusText(statusCode),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// TestVersionTags is a set of version tags for testing registry operations.
var TestVersionTags = []string{"v4.16", "v4.17", "v4.18", "v4.19", "v4.20"}

// TestMixedTags is a set of mixed tags including non-version tags.
var TestMixedTags = []string{"latest", "v4.18", "v4.19", "sha256-abc123", "v4.17", "main"}
