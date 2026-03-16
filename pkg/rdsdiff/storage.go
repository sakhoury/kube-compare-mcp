// SPDX-License-Identifier: Apache-2.0

package rdsdiff

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SessionDir creates a session directory under workDir and returns its path.
// workDir defaults to os.TempDir() if empty. Session name is rds-diff-<requestID>-<timestamp>.
func SessionDir(workDir, requestID string) (string, error) {
	if workDir == "" {
		workDir = os.TempDir()
	}
	workDir = filepath.Clean(workDir)
	if err := os.MkdirAll(workDir, 0750); err != nil {
		return "", fmt.Errorf("mkdir work dir: %w", err)
	}
	name := fmt.Sprintf("rds-diff-%s-%d", requestID, time.Now().Unix())
	sessionPath := filepath.Join(workDir, name)
	if err := os.MkdirAll(sessionPath, 0750); err != nil {
		return "", fmt.Errorf("mkdir session: %w", err)
	}
	return sessionPath, nil
}
