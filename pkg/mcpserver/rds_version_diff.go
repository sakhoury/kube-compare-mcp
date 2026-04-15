// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openshift/kube-compare/pkg/rdsdiff"
)

// RDSVersionDiffTool returns the MCP tool definition for rds-version-diff.
func RDSVersionDiffTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "kube_compare_rds_version_diff",
		Description: "Detect configuration differences between two RDS (telco reference) versions. Accepts two downloadable URLs (old and new; e.g. GitHub tree URLs or direct links to zip/tar.gz), downloads and extracts sources, runs the PolicyGenerator binary to generate policies, then compares and reports differences. Returns diff report, artifact_id for HTTP download at GET /artifacts/<artifact_id>/..., and session path.",
		InputSchema: RDSVersionDiffInputSchema(),
	}
}

// RDSVersionDiffInput is the typed input for the RDS version diff tool.
type RDSVersionDiffInput struct {
	OldVersionURL string `json:"old_version_url" jsonschema:"URL for the older telco reference (e.g. GitHub tree URL or direct link to a zip/tar.gz archive). Must be downloadable via HTTP GET."`
	NewVersionURL string `json:"new_version_url" jsonschema:"URL for the newer telco reference (e.g. GitHub tree URL or direct link to a zip/tar.gz archive). Must be downloadable via HTTP GET."`
	WorkDir       string `json:"work_dir,omitempty" jsonschema:"Optional base directory for session artifacts; defaults to RDS_DIFF_WORK_DIR env or OS temp"`
}

// RDSVersionDiffOutput is the typed output for the RDS version diff tool (for handler signature consistency).
type RDSVersionDiffOutput struct {
	// DiffReport is the full diff report text (generated CRs comparison).
	DiffReport string `json:"diff_report"`
	// ArtifactsPath is the path to the session directory containing downloaded sources, generated CRs, and the diff report file.
	ArtifactsPath string `json:"artifacts_path"`
	// ArtifactID is the session directory name; use with GET /artifacts/<artifact_id>/... to download files.
	ArtifactID string `json:"artifact_id"`
	// ArtifactsBaseURL is set when RDS_ARTIFACTS_BASE_URL env is set; base URL for artifact HTTP access (e.g. https://host/artifacts/<artifact_id>/).
	ArtifactsBaseURL string `json:"artifacts_base_url,omitempty"`
}

// HandleRDSVersionDiff is the MCP tool handler for kube_compare_rds_version_diff.
func HandleRDSVersionDiff(ctx context.Context, req *mcp.CallToolRequest, input RDSVersionDiffInput) (toolResult *mcp.CallToolResult, output RDSVersionDiffOutput, toolErr error) {
	requestID := generateRequestID()
	logger := slog.Default().With("requestID", requestID)

	defer func() {
		if r := recover(); r != nil {
			logger.Error("Panic recovered in RDS version diff handler", "panic", r, "stackTrace", string(debug.Stack()))
			toolResult = newToolResultError(fmt.Sprintf("Internal error: %v", r))
		}
	}()

	if err := ctx.Err(); err != nil {
		logger.Warn("Request canceled", "error", err)
		return newToolResultFormatError(ErrContextCanceled), RDSVersionDiffOutput{}, nil
	}

	oldURL := strings.TrimSpace(input.OldVersionURL)
	newURL := strings.TrimSpace(input.NewVersionURL)
	if err := validateRDSVersionDiffURLs(oldURL, newURL); err != nil {
		return newToolResultFormatError(err), RDSVersionDiffOutput{}, nil
	}

	workDir := resolveWorkDir(input.WorkDir)
	sessionDir, err := rdsdiff.SessionDir(workDir, requestID)
	if err != nil {
		logger.Error("Create session dir failed", "error", err)
		return newToolResultFormatError(err), RDSVersionDiffOutput{}, nil
	}

	oldDest := filepath.Join(sessionDir, "old")
	newDest := filepath.Join(sessionDir, "new")
	if err := os.MkdirAll(oldDest, 0750); err != nil {
		return newToolResultFormatError(err), RDSVersionDiffOutput{}, nil
	}
	if err := os.MkdirAll(newDest, 0750); err != nil {
		return newToolResultFormatError(err), RDSVersionDiffOutput{}, nil
	}

	client := &http.Client{Timeout: rdsdiff.DefaultDownloadTimeout}
	oldRoot, err := rdsdiff.DownloadURL(oldURL, oldDest, client)
	if err != nil {
		logger.Error("Download old version failed", "error", err, "url", oldURL)
		return newToolResultFormatError(err), RDSVersionDiffOutput{}, nil
	}
	if err := rdsdiff.ValidateConfigurationRoot(oldRoot); err != nil {
		logger.Error("Old version missing configuration layout", "error", err)
		return newToolResultFormatError(err), RDSVersionDiffOutput{}, nil
	}

	newRoot, err := rdsdiff.DownloadURL(newURL, newDest, client)
	if err != nil {
		logger.Error("Download new version failed", "error", err, "url", newURL)
		return newToolResultFormatError(err), RDSVersionDiffOutput{}, nil
	}
	if err := rdsdiff.ValidateConfigurationRoot(newRoot); err != nil {
		logger.Error("New version missing configuration layout", "error", err)
		return newToolResultFormatError(err), RDSVersionDiffOutput{}, nil
	}

	policyGenPath, err := resolvePolicyGeneratorPath()
	if err != nil {
		logger.Error("PolicyGenerator binary not found", "error", err)
		return newToolResultFormatError(err), RDSVersionDiffOutput{}, nil
	}

	oldGenerated := filepath.Join(sessionDir, "old", "generated")
	newGenerated := filepath.Join(sessionDir, "new", "generated")
	if err := rdsdiff.RunPolicyGen(oldRoot, policyGenPath, oldGenerated); err != nil {
		logger.Error("PolicyGen for old version failed", "error", err)
		return newToolResultFormatError(err), RDSVersionDiffOutput{}, nil
	}
	if err := rdsdiff.RunPolicyGen(newRoot, policyGenPath, newGenerated); err != nil {
		logger.Error("PolicyGen for new version failed", "error", err)
		return newToolResultFormatError(err), RDSVersionDiffOutput{}, nil
	}

	summary, err := rdsdiff.RunCompare(oldGenerated, newGenerated, sessionDir)
	if err != nil {
		logger.Error("RDS version compare failed", "error", err)
		return newToolResultFormatError(err), RDSVersionDiffOutput{}, nil
	}

	reportPath := filepath.Join(sessionDir, "diff-report.txt")
	fullReport := ""
	if data, err := os.ReadFile(reportPath); err == nil { // #nosec G304 -- reportPath is under sessionDir we created
		fullReport = string(data)
	}

	artifactID := rdsdiff.SessionID(sessionDir)
	output = RDSVersionDiffOutput{
		DiffReport:    fullReport,
		ArtifactsPath: sessionDir,
		ArtifactID:    artifactID,
	}

	body := summary + "\n\nArtifacts path: " + sessionDir + "\nArtifact ID: " + artifactID
	body += "\n\nDownload files at: GET /artifacts/" + artifactID + "/"
	body += "\n  Key paths: diff-report.txt, comparison.json, old/, new/, old/generated/, new/generated/, old-extracted/, new-extracted/"

	baseURL := strings.TrimSuffix(strings.TrimSpace(os.Getenv("RDS_ARTIFACTS_BASE_URL")), "/")
	if baseURL != "" {
		output.ArtifactsBaseURL = baseURL + "/artifacts/" + artifactID + "/"
		body += "\n\nArtifacts base URL: " + output.ArtifactsBaseURL
		body += "\n  Reports: " + output.ArtifactsBaseURL + "diff-report.txt, " + output.ArtifactsBaseURL + "comparison.json"
		body += "\n  RDS sources: " + output.ArtifactsBaseURL + "old/, " + output.ArtifactsBaseURL + "new/"
	}

	if fullReport != "" {
		body += "\n\n--- Diff report ---\n" + fullReport
	}

	logger.Info("RDS version diff completed", "sessionDir", sessionDir, "artifactID", artifactID)
	return newToolResultText(body), output, nil
}

func validateRDSVersionDiffURLs(oldURL, newURL string) *ValidationError {
	if oldURL == "" {
		return NewValidationError("old_version_url", "old_version_url is required", "Provide a downloadable URL (e.g. GitHub tree URL or direct link to zip/tar.gz).")
	}
	if newURL == "" {
		return NewValidationError("new_version_url", "new_version_url is required", "Provide a downloadable URL (e.g. GitHub tree URL or direct link to zip/tar.gz).")
	}
	// Basic URL shape check; actual download is attempted in the handler.
	if u, err := url.Parse(oldURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return NewValidationError("old_version_url", "invalid or unsupported URL", "Use an https or http URL that points to a downloadable archive or GitHub tree.")
	}
	if u, err := url.Parse(newURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return NewValidationError("new_version_url", "invalid or unsupported URL", "Use an https or http URL that points to a downloadable archive or GitHub tree.")
	}
	return nil
}

func resolveWorkDir(workDir string) string {
	workDir = strings.TrimSpace(workDir)
	if workDir != "" {
		return filepath.Clean(workDir)
	}
	if d := os.Getenv("RDS_DIFF_WORK_DIR"); d != "" {
		return filepath.Clean(d)
	}
	return os.TempDir()
}

// resolvePolicyGeneratorPath returns the path to the PolicyGenerator binary from POLICY_GENERATOR_BINARY_PATH env, or an error if unset/not found/not executable.
func resolvePolicyGeneratorPath() (string, error) {
	p, ok := os.LookupEnv("POLICY_GENERATOR_BINARY_PATH")
	if !ok {
		return "", fmt.Errorf("environment variable POLICY_GENERATOR_BINARY_PATH is not set")
	}
	p = filepath.Clean(strings.TrimSpace(p))
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("stat policyGenerator binary: %w", err)
	}
	return p, nil
}
