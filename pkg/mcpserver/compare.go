// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openshift/kube-compare/pkg/compare"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/rest"
	kcmdutil "k8s.io/kubectl/pkg/cmd/util"
)

const (
	DirectoryPermissions    = 0750
	FilePermissions         = 0600
	DefaultMaxFileSize      = 100 * 1024 * 1024  // 100MB default
	MaxAllowedFileSize      = 1024 * 1024 * 1024 // 1GB absolute maximum
	DefaultImagePullTimeout = 5 * time.Minute
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

// ClusterCompareInput defines the typed input for the cluster_compare tool.
// JSON Schema tags are used for automatic schema generation.
type ClusterCompareInput struct {
	Reference    string `json:"reference" jsonschema:"Reference configuration URL"`
	OutputFormat string `json:"output_format,omitempty" jsonschema:"Output format for comparison results"`
	AllResources bool   `json:"all_resources,omitempty" jsonschema:"Compare all resources of types mentioned in the reference"`
	Kubeconfig   string `json:"kubeconfig,omitempty" jsonschema:"Base64-encoded kubeconfig content for connecting to a remote cluster"`
	Context      string `json:"context,omitempty" jsonschema:"Kubernetes context name to use from the provided kubeconfig"`
}

// ClusterCompareOutput is an empty output struct (tool returns text content).
type ClusterCompareOutput struct{}

// ptrBool returns a pointer to a bool value, used for optional annotation fields.
func ptrBool(b bool) *bool {
	return &b
}

// ClusterCompareTool returns the MCP tool definition for cluster-compare.
func ClusterCompareTool() *mcp.Tool {
	return &mcp.Tool{
		Name: "cluster_compare",
		Description: "Compare Kubernetes cluster configurations against a reference configuration. " +
			"Detects configuration drift between live cluster resources and a known-good reference template. " +
			"References must be provided as HTTP/HTTPS URLs or OCI container image references.",
		InputSchema: ClusterCompareInputSchema(),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			DestructiveHint: ptrBool(false),
			IdempotentHint:  true,
			OpenWorldHint:   ptrBool(true),
		},
	}
}

// getMaxFileSize returns the maximum file size for container extraction.
// Can be configured via KUBE_COMPARE_MCP_MAX_FILE_SIZE environment variable (in bytes).
// Values exceeding MaxAllowedFileSize (1GB) are capped to prevent memory exhaustion.
func getMaxFileSize() int64 {
	if envVal := os.Getenv("KUBE_COMPARE_MCP_MAX_FILE_SIZE"); envVal != "" {
		if size, err := strconv.ParseInt(envVal, 10, 64); err == nil && size > 0 {
			if size > MaxAllowedFileSize {
				slog.Default().Warn("Requested file size exceeds maximum allowed, using limit",
					"requested", size,
					"max", MaxAllowedFileSize,
				)
				return MaxAllowedFileSize
			}
			return size
		}
	}
	return DefaultMaxFileSize
}

// getImagePullTimeout returns the timeout for pulling container images.
// Can be configured via KUBE_COMPARE_MCP_IMAGE_PULL_TIMEOUT environment variable (duration string).
func getImagePullTimeout() time.Duration {
	if envVal := os.Getenv("KUBE_COMPARE_MCP_IMAGE_PULL_TIMEOUT"); envVal != "" {
		if duration, err := time.ParseDuration(envVal); err == nil && duration > 0 {
			return duration
		}
	}
	return DefaultImagePullTimeout
}

var requestIDCounter atomic.Uint64

// generateRequestID creates a unique request ID for correlation logging.
// Thread-safe for concurrent use across HTTP handlers.
func generateRequestID() string {
	counter := requestIDCounter.Add(1)
	return fmt.Sprintf("%d-%05d", time.Now().Unix(), counter%100000)
}

// CompareService encapsulates dependencies for compare operations.
// This enables dependency injection for testing.
type CompareService struct {
	HTTPClient HTTPDoer
	Registry   RegistryClient
}

// NewCompareService creates a new CompareService with default implementations.
func NewCompareService() *CompareService {
	return &CompareService{
		HTTPClient: &DefaultHTTPDoer{Client: &http.Client{
			Timeout: getHTTPValidationTimeout(),
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		}},
		Registry: DefaultRegistry,
	}
}

var defaultCompareService = NewCompareService()

// HandleClusterCompare is the MCP tool handler for the cluster_compare tool.
// It uses typed input via the ClusterCompareInput struct.
func HandleClusterCompare(ctx context.Context, req *mcp.CallToolRequest, input ClusterCompareInput) (*mcp.CallToolResult, ClusterCompareOutput, error) {
	requestID := generateRequestID()
	logger := slog.Default().With("requestID", requestID)
	start := time.Now()

	logger.Debug("Received tool request", "tool", "cluster_compare")

	// Handle panics
	defer func() {
		if r := recover(); r != nil {
			stackTrace := string(debug.Stack())
			logger.Error("Panic recovered in tool handler",
				"panic", r,
				"stackTrace", stackTrace,
			)
		}
	}()

	if err := ctx.Err(); err != nil {
		logger.Warn("Request canceled", "error", err)
		return newToolResultError(formatErrorForUser(ErrContextCanceled)), ClusterCompareOutput{}, nil
	}

	// Convert typed input to CompareArgs
	args := &CompareArgs{
		Reference:    input.Reference,
		OutputFormat: input.OutputFormat,
		AllResources: input.AllResources,
		Kubeconfig:   input.Kubeconfig,
		Context:      input.Context,
	}

	// Validate context requires kubeconfig
	if args.Context != "" && args.Kubeconfig == "" {
		err := NewValidationError("context",
			"'context' parameter requires 'kubeconfig' to also be provided",
			"Provide a base64-encoded kubeconfig along with the context name")
		logger.Debug("Validation failed", "error", err)
		return newToolResultError(formatErrorForUser(err)), ClusterCompareOutput{}, nil
	}

	logger.Debug("Parsed compare arguments",
		"reference", args.Reference,
		"outputFormat", args.OutputFormat,
		"allResources", args.AllResources,
		"hasKubeconfig", args.Kubeconfig != "",
		"context", args.Context,
	)

	if err := validateReference(ctx, args); err != nil {
		logger.Debug("Reference validation failed", "error", err)
		return newToolResultError(formatErrorForUser(err)), ClusterCompareOutput{}, nil
	}

	logger.Info("Starting cluster comparison", "reference", args.Reference)
	output, err := RunCompare(ctx, args)
	duration := time.Since(start)

	if err != nil {
		logger.Error("Comparison failed",
			"error", err,
			"duration", duration,
			"reference", args.Reference,
		)
		return newToolResultError(formatErrorForUser(err)), ClusterCompareOutput{}, nil
	}

	logger.Info("Comparison completed",
		"duration", duration,
		"reference", args.Reference,
		"outputLength", len(output),
	)

	return newToolResultText(output), ClusterCompareOutput{}, nil
}

// ExtractArguments safely extracts the arguments map from the MCP request.
// This function is maintained for backward compatibility with tests.
// With the official SDK's typed handlers, argument extraction is automatic.
func ExtractArguments(req *mcp.CallToolRequest) (map[string]interface{}, error) {
	if len(req.Params.Arguments) == 0 {
		// Return empty map for nil/empty arguments - not an error
		return make(map[string]interface{}), nil
	}

	var arguments map[string]interface{}
	if err := json.Unmarshal(req.Params.Arguments, &arguments); err != nil {
		return nil, NewValidationError("arguments", "invalid argument format", "arguments must be a JSON object")
	}

	return arguments, nil
}

// CompareArgs holds the parsed arguments for the compare operation.
type CompareArgs struct {
	Reference    string
	OutputFormat string
	AllResources bool
	Kubeconfig   string // Base64-encoded kubeconfig content (optional)
	Context      string // Kubernetes context name to use (optional)
}

// GetStringArg safely extracts a string argument with proper type checking.
func GetStringArg(args map[string]interface{}, key string, required bool) (string, error) {
	val, exists := args[key]
	if !exists || val == nil {
		if required {
			return "", NewValidationError(key, "required parameter is missing", fmt.Sprintf("provide the '%s' parameter", key))
		}
		return "", nil
	}

	str, ok := val.(string)
	if !ok {
		return "", NewValidationError(key, fmt.Sprintf("expected string but got %T", val), "provide a string value")
	}

	if required && strings.TrimSpace(str) == "" {
		return "", NewValidationError(key, "required parameter is empty", fmt.Sprintf("provide a non-empty value for '%s'", key))
	}

	return strings.TrimSpace(str), nil
}

// GetBoolArg safely extracts a boolean argument with proper type checking.
func GetBoolArg(args map[string]interface{}, key string, defaultVal bool) (bool, error) {
	val, exists := args[key]
	if !exists || val == nil {
		return defaultVal, nil
	}

	b, ok := val.(bool)
	if !ok {
		return defaultVal, NewValidationError(key, fmt.Sprintf("expected boolean but got %T", val), "provide true or false")
	}

	return b, nil
}

// validateReference validates the reference configuration path/URL.
func validateReference(ctx context.Context, args *CompareArgs) error {
	refType := ClassifyReference(args.Reference)

	switch refType {
	case ReferenceTypeLocal:
		return NewCompareError("validate",
			ErrLocalPathNotSupported,
			fmt.Sprintf("Local filesystem paths are not supported in remote deployments. "+
				"The reference '%s' appears to be a local path.\n\n"+
				"Please provide a remote reference using one of these formats:\n"+
				"- HTTP/HTTPS URL: https://example.com/path/to/metadata.yaml\n"+
				"- OCI container image: container://quay.io/org/refs:v1.0:/path/to/metadata.yaml",
				args.Reference))

	case ReferenceTypeHTTP:
		return validateHTTPReference(ctx, args.Reference)

	case ReferenceTypeOCI:
		return validateOCIReference(ctx, args.Reference)

	default:
		return NewValidationError("reference",
			"unknown reference type",
			"use an HTTP/HTTPS URL or container:// image reference")
	}
}

type ReferenceType int

const (
	ReferenceTypeLocal ReferenceType = iota
	ReferenceTypeHTTP
	ReferenceTypeOCI
)

// ClassifyReference determines the type of reference from the input string.
func ClassifyReference(ref string) ReferenceType {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ReferenceTypeHTTP
	}
	if strings.HasPrefix(ref, "container://") {
		return ReferenceTypeOCI
	}
	return ReferenceTypeLocal
}

const defaultHTTPValidationTimeout = 10 * time.Second

// getHTTPValidationTimeout returns the timeout for HTTP reference validation.
// Can be configured via KUBE_COMPARE_MCP_HTTP_VALIDATION_TIMEOUT environment variable.
func getHTTPValidationTimeout() time.Duration {
	if val := os.Getenv("KUBE_COMPARE_MCP_HTTP_VALIDATION_TIMEOUT"); val != "" {
		if duration, err := time.ParseDuration(val); err == nil && duration > 0 {
			return duration
		}
	}
	return defaultHTTPValidationTimeout
}

func validateHTTPReference(ctx context.Context, refURL string) error {
	return defaultCompareService.ValidateHTTPReference(ctx, refURL)
}

// ValidateHTTPReference validates that an HTTP/HTTPS URL is reachable using the injected HTTP client.
func (s *CompareService) ValidateHTTPReference(ctx context.Context, refURL string) error {
	logger := slog.Default()
	logger.Debug("Validating HTTP reference", "url", refURL)

	validateCtx, cancel := context.WithTimeout(ctx, getHTTPValidationTimeout())
	defer cancel()

	req, err := http.NewRequestWithContext(validateCtx, http.MethodHead, refURL, nil)
	if err != nil {
		return NewValidationError("reference",
			fmt.Sprintf("invalid HTTP URL: %v", err),
			"Provide a valid HTTP/HTTPS URL to the metadata.yaml file")
	}

	req.Header.Set("User-Agent", "kube-compare-mcp/1.0")

	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return NewCompareError("validate", ErrContextCanceled, "The validation was canceled")
		}

		return NewCompareError("validate",
			fmt.Errorf("%w: %w", ErrRemoteUnreachable, err),
			fmt.Sprintf("Could not reach '%s'. Verify the URL is accessible from the cluster and the server is running.", refURL))
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		statusText := http.StatusText(resp.StatusCode)

		// Read up to 1KB of response body for error context
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		bodyHint := strings.TrimSpace(string(bodyBytes))

		switch resp.StatusCode {
		case http.StatusNotFound:
			msg := fmt.Sprintf("The reference at '%s' was not found. Verify the URL path is correct.", refURL)
			if bodyHint != "" {
				msg += fmt.Sprintf(" Server response: %s", bodyHint)
			}
			return NewCompareError("validate",
				fmt.Errorf("%w: HTTP 404 Not Found", ErrReferenceNotFound),
				msg)
		case http.StatusUnauthorized, http.StatusForbidden:
			msg := fmt.Sprintf("Access denied to '%s'. The reference may require authentication.", refURL)
			if bodyHint != "" {
				msg += fmt.Sprintf(" Server response: %s", bodyHint)
			}
			return NewCompareError("validate",
				fmt.Errorf("%w: HTTP %d %s", ErrRemoteUnreachable, resp.StatusCode, statusText),
				msg)
		default:
			msg := fmt.Sprintf("The server returned an error for '%s'. Verify the URL is correct.", refURL)
			if bodyHint != "" {
				msg += fmt.Sprintf(" Server response: %s", bodyHint)
			}
			return NewCompareError("validate",
				fmt.Errorf("%w: HTTP %d %s", ErrRemoteUnreachable, resp.StatusCode, statusText),
				msg)
		}
	}

	logger.Debug("HTTP reference validated successfully", "url", refURL, "status", resp.StatusCode)
	return nil
}

const defaultOCIValidationTimeout = 30 * time.Second

// getOCIValidationTimeout returns the timeout for OCI reference validation.
// Can be configured via KUBE_COMPARE_MCP_OCI_VALIDATION_TIMEOUT environment variable.
func getOCIValidationTimeout() time.Duration {
	if val := os.Getenv("KUBE_COMPARE_MCP_OCI_VALIDATION_TIMEOUT"); val != "" {
		if duration, err := time.ParseDuration(val); err == nil && duration > 0 {
			return duration
		}
	}
	return defaultOCIValidationTimeout
}

func validateOCIReference(ctx context.Context, ref string) error {
	return defaultCompareService.ValidateOCIReference(ctx, ref)
}

// ValidateOCIReference validates that an OCI container image exists using the injected registry client.
func (s *CompareService) ValidateOCIReference(ctx context.Context, ref string) error {
	logger := slog.Default()
	logger.Debug("Validating OCI reference", "ref", ref)

	imageRef, filePath, err := ParseContainerReference(ref)
	if err != nil {
		return err
	}

	logger.Debug("Parsed container reference", "image", imageRef, "path", filePath)

	_, err = name.ParseReference(imageRef)
	if err != nil {
		return NewValidationError("reference",
			fmt.Sprintf("invalid container image reference '%s': %v", imageRef, err),
			"Use format: container://registry/image:tag:/path/to/metadata.yaml")
	}

	validateCtx, cancel := context.WithTimeout(ctx, getOCIValidationTimeout())
	defer cancel()

	err = s.Registry.HeadImage(validateCtx, imageRef)
	if err != nil {
		if ctx.Err() != nil {
			return NewCompareError("validate", ErrContextCanceled, "The validation was canceled")
		}

		// Check for common error patterns
		errStr := err.Error()
		if strings.Contains(errStr, "MANIFEST_UNKNOWN") || strings.Contains(errStr, "NAME_UNKNOWN") {
			return NewCompareError("validate",
				fmt.Errorf("%w: %s", ErrOCIImageNotFound, imageRef),
				fmt.Sprintf("The container image '%s' was not found. Verify the image name, tag, and registry.", imageRef))
		}
		if strings.Contains(errStr, "UNAUTHORIZED") || strings.Contains(errStr, "DENIED") {
			return NewCompareError("validate",
				fmt.Errorf("%w: authentication failed for %s", ErrRemoteUnreachable, imageRef),
				"Access denied to the container registry. The image may require authentication or be in a private repository.")
		}
		if strings.Contains(errStr, "no such host") || strings.Contains(errStr, "connection refused") {
			return NewCompareError("validate",
				fmt.Errorf("%w: cannot reach registry for %s", ErrRemoteUnreachable, imageRef),
				"Could not connect to the container registry. Verify the registry URL and network connectivity.")
		}

		return NewCompareError("validate",
			fmt.Errorf("%w: %w", ErrRemoteUnreachable, err),
			fmt.Sprintf("Failed to validate container image '%s'. Verify the image reference is correct.", imageRef))
	}

	logger.Debug("OCI reference validated successfully", "image", imageRef)
	return nil
}

// ParseContainerReference parses a container:// reference into image and file path.
func ParseContainerReference(ref string) (imageRef, filePath string, err error) {
	const prefix = "container://"
	if !strings.HasPrefix(ref, prefix) {
		return "", "", NewValidationError("reference",
			"invalid container reference format",
			"Use format: container://registry/image:tag:/path/to/metadata.yaml")
	}

	remainder := strings.TrimPrefix(ref, prefix)

	// Find the :/ pattern that indicates the start of the file path.
	// The format is: image:tag:/path or image:/path (using latest tag).
	pathSepIdx := -1
	for i := 0; i < len(remainder)-1; i++ {
		if remainder[i] != ':' || remainder[i+1] != '/' {
			continue
		}
		// Check if this could be a port number or tag
		// If there's already been a : before, this is likely the path separator
		prevColonIdx := strings.LastIndex(remainder[:i], ":")
		if prevColonIdx >= 0 {
			// There was a previous colon (likely for tag), so this :/ is the path
			pathSepIdx = i
			break
		}
		// Check if the part before : looks like a tag (no slashes after it)
		afterColon := remainder[i+1:]
		nextSlash := strings.Index(afterColon, "/")
		if nextSlash == 0 {
			// Immediately followed by /, this is the path separator
			pathSepIdx = i
			break
		}
	}

	if pathSepIdx == -1 {
		return "", "", NewValidationError("reference",
			"missing file path in container reference",
			"Use format: container://registry/image:tag:/path/to/metadata.yaml")
	}

	imageRef = remainder[:pathSepIdx]
	filePath = remainder[pathSepIdx+1:] // Include the leading /

	if imageRef == "" {
		return "", "", NewValidationError("reference",
			"missing image reference",
			"Use format: container://registry/image:tag:/path/to/metadata.yaml")
	}

	if filePath == "" || filePath == "/" {
		return "", "", NewValidationError("reference",
			"missing or invalid file path",
			"Specify the path to metadata.yaml within the container image")
	}

	return imageRef, filePath, nil
}

// processTarEntry handles extracting a single tar entry to the destination directory.
// Returns the number of files extracted (0 for directories/symlinks, 1 for regular files) and any error.
func processTarEntry(header *tar.Header, tr *tar.Reader, destPath string, logger *slog.Logger) (int, error) {
	switch header.Typeflag {
	case tar.TypeDir:
		if err := os.MkdirAll(destPath, DirectoryPermissions); err != nil {
			return 0, fmt.Errorf("failed to create directory %s: %w", destPath, err)
		}
		return 0, nil

	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(destPath), DirectoryPermissions); err != nil {
			return 0, fmt.Errorf("failed to create parent directory for %s: %w", destPath, err)
		}

		// #nosec G304 -- destPath is validated against path traversal attacks by caller
		f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, FilePermissions)
		if err != nil {
			return 0, fmt.Errorf("failed to create file %s: %w", destPath, err)
		}

		maxFileSize := getMaxFileSize()
		written, err := io.CopyN(f, tr, maxFileSize)
		if err != nil && !errors.Is(err, io.EOF) {
			_ = f.Close()
			return 0, fmt.Errorf("failed to write file %s: %w", destPath, err)
		}
		_ = f.Close()

		if written > 0 {
			logger.Debug("Extracted file", "path", header.Name, "size", written)
		}
		return 1, nil

	case tar.TypeSymlink:
		if err := os.MkdirAll(filepath.Dir(destPath), DirectoryPermissions); err != nil {
			return 0, fmt.Errorf("failed to create parent directory for symlink %s: %w", destPath, err)
		}
		_ = os.Remove(destPath)
		if err := os.Symlink(header.Linkname, destPath); err != nil {
			logger.Debug("Failed to create symlink", "path", destPath, "target", header.Linkname, "error", err)
		}
		return 0, nil

	default:
		// Skip unsupported file types (block devices, char devices, etc.)
		return 0, nil
	}
}

// extractContainerReference extracts files from a container image to a local directory.
func extractContainerReference(ctx context.Context, imageRef, targetPath, destDir string) (string, error) {
	logger := slog.Default()
	logger.Debug("Extracting container reference", "image", imageRef, "targetPath", targetPath)

	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return "", fmt.Errorf("invalid image reference '%s': %w", imageRef, err)
	}

	pullTimeout := getImagePullTimeout()
	pullCtx, cancel := context.WithTimeout(ctx, pullTimeout)
	defer cancel()

	logger.Debug("Pulling container image", "image", imageRef, "timeout", pullTimeout)

	img, err := remote.Image(ref,
		remote.WithContext(pullCtx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	)
	if err != nil {
		if pullCtx.Err() != nil {
			return "", fmt.Errorf("image pull timed out after %v for '%s': %w", pullTimeout, imageRef, err)
		}
		return "", fmt.Errorf("failed to pull image '%s': %w", imageRef, err)
	}

	logger.Debug("Image pulled successfully", "image", imageRef)

	reader := mutate.Extract(img)
	defer reader.Close()

	tr := tar.NewReader(reader)

	// Normalize target path and extract files matching the target directory
	targetPath = strings.TrimPrefix(targetPath, "/")
	targetDir := filepath.Dir(targetPath)
	extractedFiles := 0
	for {
		// Check for context cancellation to avoid wasting resources if client disconnected
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("extraction canceled: %w", ctx.Err())
		default:
		}

		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("error reading tar: %w", err)
		}

		fileName := strings.TrimPrefix(header.Name, "./")
		fileName = strings.TrimPrefix(fileName, "/")

		if !strings.HasPrefix(fileName, targetDir) {
			continue
		}

		destPath := filepath.Join(destDir, fileName)

		// Security: Validate that the resolved path is within destDir to prevent path traversal
		cleanDest := filepath.Clean(destPath)
		cleanBase := filepath.Clean(destDir) + string(filepath.Separator)
		if !strings.HasPrefix(cleanDest, cleanBase) && cleanDest != filepath.Clean(destDir) {
			logger.Warn("Skipping path traversal attempt", "path", header.Name, "resolved", cleanDest)
			continue
		}

		filesAdded, err := processTarEntry(header, tr, destPath, logger)
		if err != nil {
			return "", err
		}
		extractedFiles += filesAdded
	}

	logger.Info("Container extraction complete", "image", imageRef, "filesExtracted", extractedFiles)

	extractedPath := filepath.Join(destDir, targetPath)
	if _, err := os.Stat(extractedPath); os.IsNotExist(err) {
		return "", fmt.Errorf("target file not found in container image: %s", targetPath)
	}

	return extractedPath, nil
}

// RunCompare executes the kube-compare operation and returns the result.
func RunCompare(ctx context.Context, args *CompareArgs) (string, error) {
	logger := slog.Default()

	if err := ctx.Err(); err != nil {
		return "", NewCompareError("run", ErrContextCanceled, "The operation was canceled before comparison started")
	}

	tmpDir, err := os.MkdirTemp("", "kube-compare-mcp")
	if err != nil {
		return "", NewCompareError("initialize",
			fmt.Errorf("failed to create temp directory: %w", err),
			"Check that the system temp directory is writable")
	}
	defer func() {
		if removeErr := os.RemoveAll(tmpDir); removeErr != nil {
			// Log but don't fail - this is cleanup
			logger.Warn("Failed to clean up temp directory",
				"tmpDir", tmpDir,
				"error", removeErr,
			)
		}
	}()

	// Handle container:// references by extracting them locally
	referenceConfig := args.Reference
	if ClassifyReference(args.Reference) == ReferenceTypeOCI {
		logger.Info("Extracting container reference using go-containerregistry")

		imageRef, filePath, err := ParseContainerReference(args.Reference)
		if err != nil {
			return "", NewCompareError("initialize", err, "Failed to parse container reference")
		}

		// Extract the container image to the temp directory
		extractDir := filepath.Join(tmpDir, "extracted")
		if err := os.MkdirAll(extractDir, DirectoryPermissions); err != nil {
			return "", NewCompareError("initialize",
				fmt.Errorf("failed to create extraction directory: %w", err),
				"Check filesystem permissions")
		}

		extractedPath, err := extractContainerReference(ctx, imageRef, filePath, extractDir)
		if err != nil {
			return "", NewCompareError("initialize",
				fmt.Errorf("failed to extract container reference: %w", err),
				"Verify the container image and path are correct. Check registry authentication if needed.")
		}

		logger.Info("Container reference extracted", "extractedPath", extractedPath)
		referenceConfig = extractedPath
	}

	var outBuf, errBuf bytes.Buffer
	ioStreams := genericiooptions.IOStreams{
		In:     os.Stdin,
		Out:    &outBuf,
		ErrOut: &errBuf,
	}

	opts := compare.NewOptions(ioStreams)
	opts.ReferenceConfig = referenceConfig
	opts.OutputFormat = args.OutputFormat
	opts.TmpDir = tmpDir

	var configFlags *genericclioptions.ConfigFlags
	if args.Kubeconfig != "" {
		logger.Info("Using provided kubeconfig for cluster connection")
		restConfig, err := BuildSecureRestConfig(args.Kubeconfig, args.Context)
		if err != nil {
			return "", err
		}

		configFlags = genericclioptions.NewConfigFlags(true)
		configFlags.WithWrapConfigFn(func(config *rest.Config) *rest.Config {
			config.Host = restConfig.Host
			config.TLSClientConfig = restConfig.TLSClientConfig
			config.BearerToken = restConfig.BearerToken
			config.BearerTokenFile = restConfig.BearerTokenFile
			config.Username = restConfig.Username
			config.Password = restConfig.Password
			config.Impersonate = restConfig.Impersonate
			config.CertData = restConfig.CertData
			config.KeyData = restConfig.KeyData
			config.CAData = restConfig.CAData
			return config
		})
	} else {
		logger.Debug("Using default cluster credentials")
		configFlags = genericclioptions.NewConfigFlags(true)
	}
	factory := kcmdutil.NewFactory(configFlags)

	if err := opts.Complete(factory, nil, nil); err != nil {
		errOutput := errBuf.String()
		details := BuildErrorDetails(err, errOutput)
		return "", NewCompareError("initialize", err, details)
	}

	if err := ctx.Err(); err != nil {
		return "", NewCompareError("run", ErrContextCanceled, "The operation was canceled during initialization")
	}

	runErr := opts.Run()
	output := outBuf.String()
	errOutput := errBuf.String()

	return ProcessCompareResult(output, errOutput, runErr)
}

// BuildErrorDetails creates a helpful error message based on the error and context.
func BuildErrorDetails(err error, errOutput string) string {
	var details strings.Builder

	errStr := err.Error()

	// Detect common error patterns and provide helpful suggestions
	switch {
	case strings.Contains(errStr, "no such file or directory"):
		details.WriteString("The reference configuration could not be found. ")
		details.WriteString("Verify that the URL is correct and accessible.\n")
	case strings.Contains(errStr, "connection refused") || strings.Contains(errStr, "no such host"):
		details.WriteString("Could not connect to the Kubernetes cluster. ")
		details.WriteString("Verify that the server has access to the cluster via in-cluster config or KUBECONFIG.\n")
	case strings.Contains(errStr, "unauthorized") || strings.Contains(errStr, "forbidden"):
		details.WriteString("Authentication or authorization failed. ")
		details.WriteString("Verify that the server's service account has the necessary permissions.\n")
	case strings.Contains(errStr, "metadata.yaml") || strings.Contains(errStr, "invalid reference"):
		details.WriteString("The reference configuration appears to be invalid. ")
		details.WriteString("Verify that the metadata.yaml file is properly formatted.\n")
	}

	// Include stderr output if available
	if errOutput != "" {
		details.WriteString("\nAdditional output:\n")
		details.WriteString(errOutput)
	}

	return details.String()
}

// ProcessCompareResult handles the comparison result and formats the output.
func ProcessCompareResult(output, errOutput string, runErr error) (string, error) {
	if output != "" {
		if runErr != nil && !IsDifferencesFoundError(runErr) {
			return fmt.Sprintf("%s\n\nWarning: Comparison completed with errors: %v", output, runErr), nil
		}
		return output, nil
	}

	if runErr != nil {
		if IsDifferencesFoundError(runErr) {
			return "Differences were found but no detailed output was generated.", nil
		}
		details := BuildErrorDetails(runErr, errOutput)
		return "", NewCompareError("compare", runErr, details)
	}

	return "No differences found between the cluster configuration and reference.", nil
}

// IsDifferencesFoundError checks if the error indicates differences were found (not a failure).
func IsDifferencesFoundError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "there are differences") ||
		strings.Contains(errStr, "differences were found")
}
