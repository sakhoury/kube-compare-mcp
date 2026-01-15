// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"log/slog"

	"github.com/mark3labs/mcp-go/server"
)

// ServerName is the name of the MCP server.
const ServerName = "kube-compare-mcp"

// NewServer creates a new MCP server with the cluster-compare tool registered.
// The version parameter should be passed from the build-time version in main.go.
func NewServer(version string) *server.MCPServer {
	logger := slog.Default()

	logger.Debug("Creating MCP server",
		"name", ServerName,
		"version", version,
	)

	s := server.NewMCPServer(
		ServerName,
		version,
		server.WithToolCapabilities(true),
	)

	s.AddTool(ClusterCompareTool(), HandleClusterCompare)
	s.AddTool(FindRDSReferenceTool(), HandleFindRDSReference)
	s.AddTool(CompareClusterRDSTool(), HandleCompareClusterRDS)

	logger.Info("MCP server initialized",
		"name", ServerName,
		"version", version,
		"tools", []string{"cluster_compare", "find_rds_reference", "compare_cluster_rds"},
	)

	return s
}
