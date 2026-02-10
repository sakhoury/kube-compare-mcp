// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newToolResultText creates a successful tool result with text content.
func newToolResultText(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: text},
		},
	}
}

// newToolResultError creates an error tool result with the given message.
func newToolResultError(errMsg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: errMsg},
		},
		IsError: true,
	}
}

// ptrBool returns a pointer to a bool value, used for optional annotation fields.
func ptrBool(b bool) *bool {
	return &b
}

var requestIDCounter atomic.Uint64

// generateRequestID creates a unique request ID for correlation logging.
// Thread-safe for concurrent use across HTTP handlers.
func generateRequestID() string {
	counter := requestIDCounter.Add(1)
	return fmt.Sprintf("%d-%05d", time.Now().Unix(), counter%100000)
}
