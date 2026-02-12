// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/rest"
)

var (
	majorMinorVersionRegex = regexp.MustCompile(`^(\d+)\.(\d+)`)
	versionTagRegex        = regexp.MustCompile(`^v\d+\.\d+$`)
)

const (
	RDSTypeCore     = "core"
	RDSTypeRAN      = "ran"
	registryTimeout = 30 * time.Second
)

// RDSConfig holds the configuration for an RDS reference type.
type RDSConfig struct {
	ImageBase    string   // e.g., "registry.redhat.io/openshift4/openshift-telco-core-rds"
	Path         string   // Path to metadata.yaml within the container
	RHELVariants []string // RHEL variants to try in order of preference (e.g., ["rhel9", "rhel8"])
}

var rdsConfigs = map[string]RDSConfig{
	RDSTypeCore: {
		ImageBase:    "registry.redhat.io/openshift4/openshift-telco-core-rds",
		Path:         "/usr/share/telco-core-rds/configuration/reference-crs-kube-compare/metadata.yaml",
		RHELVariants: []string{"rhel9", "rhel8"},
	},
	RDSTypeRAN: {
		ImageBase:    "registry.redhat.io/openshift4/ztp-site-generate",
		Path:         "/home/ztp/reference/metadata.yaml",
		RHELVariants: []string{"rhel8"},
	},
}

// ResolveRDSResult is the structured response for the kube_compare_resolve_rds tool.
type ResolveRDSResult struct {
	ClusterVersion    string   `json:"cluster_version"`
	RHELVersion       string   `json:"rhel_version"`
	RDSType           string   `json:"rds_type"`
	Reference         string   `json:"reference"`
	AvailableVersions []string `json:"available_versions"`
	Validated         bool     `json:"validated"`
}

// ReferenceService encapsulates dependencies for RDS reference operations.
// This enables dependency injection for testing.
type ReferenceService struct {
	Registry       RegistryClient
	ClusterFactory ClusterClientFactory
}

// NewReferenceService creates a new ReferenceService with default implementations.
func NewReferenceService() *ReferenceService {
	return &ReferenceService{
		Registry:       DefaultRegistry,
		ClusterFactory: DefaultClusterFactory,
	}
}

var defaultReferenceService = NewReferenceService()

// ResolveRDSInput defines the typed input for the kube_compare_resolve_rds tool.
type ResolveRDSInput struct {
	Kubeconfig string `json:"kubeconfig,omitempty" jsonschema:"Kubeconfig content (raw YAML or base64-encoded) for connecting to the target cluster. If omitted, uses in-cluster config."`
	Context    string `json:"context,omitempty" jsonschema:"Kubernetes context name to use from the provided kubeconfig"`
	RDSType    string `json:"rds_type" jsonschema:"RDS type to find: core for Telco Core RDS or ran for Telco RAN DU RDS"`
	OCPVersion string `json:"ocp_version,omitempty" jsonschema:"OpenShift version (e.g. 4.18 or 4.20.0)"`
}

// ResolveRDSOutput is an empty output struct (tool returns text content).
type ResolveRDSOutput struct{}

// ResolveRDSTool returns the MCP tool definition for finding RDS references.
func ResolveRDSTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "kube_compare_resolve_rds",
		Description: "Get the correct Red Hat Telco RDS container reference for a cluster's OpenShift version.",
		InputSchema: ResolveRDSInputSchema(),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			DestructiveHint: ptrBool(false),
			IdempotentHint:  true,
			OpenWorldHint:   ptrBool(true),
		},
	}
}

// HandleResolveRDS is the MCP tool handler for the kube_compare_resolve_rds tool.
// It uses typed input via the ResolveRDSInput struct.
func HandleResolveRDS(ctx context.Context, req *mcp.CallToolRequest, input ResolveRDSInput) (toolResult *mcp.CallToolResult, resolveOutput ResolveRDSOutput, toolErr error) {
	requestID := generateRequestID()
	logger := slog.Default().With("requestID", requestID)
	start := time.Now()

	logger.Debug("Received tool request", "tool", "kube_compare_resolve_rds")

	// Handle panics
	defer func() {
		if r := recover(); r != nil {
			stackTrace := string(debug.Stack())
			logger.Error("Panic recovered in tool handler",
				"panic", r,
				"stackTrace", stackTrace,
			)
			toolResult = newToolResultError(fmt.Sprintf("Internal error: %v", r))
		}
	}()

	if err := ctx.Err(); err != nil {
		logger.Warn("Request canceled", "error", err)
		return newToolResultError(formatErrorForUser(ErrContextCanceled)), ResolveRDSOutput{}, nil
	}

	// Validate context requires kubeconfig
	if input.Context != "" && input.Kubeconfig == "" {
		err := NewValidationError("context",
			"'context' parameter requires 'kubeconfig' to also be provided",
			"Provide a kubeconfig along with the context name")
		logger.Debug("Validation failed", "error", err)
		return newToolResultError(formatErrorForUser(err)), ResolveRDSOutput{}, nil
	}

	// Convert typed input to ResolveRDSArgs
	// Note: SDK validates enum constraint, so RDSType is already lowercase ("core" or "ran")
	args := &ResolveRDSArgs{
		Kubeconfig: input.Kubeconfig,
		Context:    input.Context,
		RDSType:    input.RDSType,
		OCPVersion: input.OCPVersion,
	}

	logger.Debug("Parsed kube_compare_resolve_rds arguments",
		"rdsType", args.RDSType,
		"hasKubeconfig", args.Kubeconfig != "",
		"context", args.Context,
		"explicitOCPVersion", args.OCPVersion,
	)

	resultData, err := ResolveRDSInternal(ctx, args)
	if err != nil {
		logger.Debug("Failed to find RDS reference", "error", err)
		return newToolResultError(formatErrorForUser(err)), ResolveRDSOutput{}, nil
	}

	jsonOutput, err := json.MarshalIndent(resultData, "", "  ")
	if err != nil {
		logger.Error("Failed to marshal result", "error", err)
		return newToolResultError(fmt.Sprintf("Failed to format result: %v", err)), ResolveRDSOutput{}, nil
	}

	duration := time.Since(start)
	logger.Info("RDS reference found",
		"duration", duration,
		"rdsType", args.RDSType,
		"clusterVersion", resultData.ClusterVersion,
		"rhelVersion", resultData.RHELVersion,
		"reference", resultData.Reference,
		"validated", resultData.Validated,
	)

	return newToolResultText(string(jsonOutput)), ResolveRDSOutput{}, nil
}

// ResolveRDSInternal is the core logic for finding RDS references.
func ResolveRDSInternal(ctx context.Context, args *ResolveRDSArgs) (*ResolveRDSResult, error) {
	return defaultReferenceService.ResolveRDS(ctx, args)
}

// ResolveRDS finds the RDS reference for the given arguments.
func (s *ReferenceService) ResolveRDS(ctx context.Context, args *ResolveRDSArgs) (*ResolveRDSResult, error) {
	logger := slog.Default()

	var clusterVersion string

	// Use explicit version if provided, otherwise auto-detect from cluster
	if args.OCPVersion != "" {
		clusterVersion = args.OCPVersion
		logger.Debug("Using explicit OCP version", "ocpVersion", clusterVersion)
	} else {
		var restConfig *rest.Config
		var err error

		if args.Kubeconfig != "" {
			logger.Debug("Using provided kubeconfig for version detection")

			// Use DecodeOrParseKubeconfig to support both raw YAML and base64-encoded kubeconfig
			kubeconfigData, err := DecodeOrParseKubeconfig(args.Kubeconfig)
			if err != nil {
				return nil, err
			}

			restConfig, err = BuildSecureRestConfigFromBytes(kubeconfigData, args.Context)
			if err != nil {
				return nil, err
			}
		} else {
			logger.Debug("Using in-cluster config for version detection")
			restConfig, err = rest.InClusterConfig()
			if err != nil {
				return nil, NewCompareError("cluster-config",
					fmt.Errorf("failed to get in-cluster config: %w", err),
					"No kubeconfig provided and in-cluster config not available. "+
						"Either provide a kubeconfig, specify ocp_version explicitly, or ensure the server is running inside a Kubernetes cluster.")
			}
		}

		// Get cluster version using the injected factory
		clusterClient, err := s.ClusterFactory.NewClient(restConfig)
		if err != nil {
			return nil, NewCompareError("cluster-version",
				fmt.Errorf("failed to create cluster client: %w", err),
				"Verify the kubeconfig is valid and has cluster access")
		}

		clusterVersion, err = clusterClient.GetClusterVersion(ctx)
		if err != nil {
			return nil, NewCompareError("cluster-version",
				fmt.Errorf("failed to get ClusterVersion: %w", err),
				"Verify the cluster is an OpenShift cluster and you have permission to read ClusterVersion")
		}

		logger.Debug("Got cluster version", "version", clusterVersion)
	}

	ocpVersion := ExtractMajorMinorVersion(clusterVersion)
	cfg := rdsConfigs[args.RDSType]

	rhelVariant, repoRef, versionTags, err := s.findBestRHELVariant(ctx, cfg, ocpVersion)
	if err != nil {
		logger.Debug("Failed to find RHEL variant", "error", err)
		return nil, err
	}

	logger.Debug("Found best RHEL variant",
		"rhelVariant", rhelVariant,
		"repoRef", repoRef,
		"ocpVersion", ocpVersion,
	)

	reference := BuildRDSReference(args.RDSType, rhelVariant, ocpVersion)

	// Validate image accessibility before returning
	imageRef := fmt.Sprintf("%s:%s", repoRef, ocpVersion)
	if err := s.Registry.HeadImage(ctx, imageRef); err != nil {
		return nil, NewCompareError("registry",
			fmt.Errorf("rds image found but not accessible: %s", ocpVersion),
			fmt.Sprintf("Image: %s\nError: %v\n\nThis may be an authentication issue. Ensure the server has credentials for registry.redhat.io.",
				imageRef, err))
	}

	return &ResolveRDSResult{
		ClusterVersion:    clusterVersion,
		RHELVersion:       rhelVariant,
		RDSType:           args.RDSType,
		Reference:         reference,
		AvailableVersions: versionTags,
		Validated:         true,
	}, nil
}

// findBestRHELVariant finds the best RHEL variant for a given RDS config and OCP version.
func (s *ReferenceService) findBestRHELVariant(ctx context.Context, cfg RDSConfig, ocpVersion string) (rhelVariant, repoRef string, versionTags []string, err error) {
	logger := slog.Default()

	var lastErr error
	var allVersionsFound []string

	listCtx, cancel := context.WithTimeout(ctx, registryTimeout)
	defer cancel()

	for _, rhel := range cfg.RHELVariants {
		repoRef := fmt.Sprintf("%s-%s", cfg.ImageBase, rhel)
		logger.Debug("Trying RHEL variant", "variant", rhel, "repo", repoRef)

		tags, err := s.Registry.ListTags(listCtx, repoRef)
		if err != nil {
			logger.Debug("Failed to list tags for variant", "variant", rhel, "error", err)
			lastErr = wrapRegistryError(err, repoRef)
			continue
		}

		versions := FilterVersionTags(tags)
		logger.Debug("Found version tags", "variant", rhel, "count", len(versions), "versions", versions)

		// Track all versions for error reporting
		if len(allVersionsFound) == 0 {
			allVersionsFound = versions
		}

		if ContainsTag(versions, ocpVersion) {
			logger.Debug("Found matching RHEL variant", "variant", rhel, "version", ocpVersion)
			return rhel, repoRef, versions, nil
		}
	}

	if lastErr != nil {
		return "", "", nil, NewCompareError("registry",
			fmt.Errorf("could not find RDS image for OpenShift %s", ocpVersion),
			fmt.Sprintf("Failed to access container registry: %v\n\nThis may be an authentication issue.", lastErr))
	}

	return "", "", nil, NewCompareError("registry",
		fmt.Errorf("rds image not found for OpenShift %s", ocpVersion),
		fmt.Sprintf("Expected image tag: %s\nRDS type image base: %s\nTried RHEL variants: %v\n\nAvailable versions:\n  %s\n\nThe requested version may not be released yet.",
			ocpVersion, cfg.ImageBase, cfg.RHELVariants, strings.Join(allVersionsFound, "\n  ")))
}

// wrapRegistryError wraps registry errors with user-friendly messages.
func wrapRegistryError(err error, repoRef string) error {
	errStr := err.Error()
	if strings.Contains(errStr, "UNAUTHORIZED") || strings.Contains(errStr, "DENIED") {
		return NewCompareError("registry-list",
			fmt.Errorf("authentication failed for %s: %w", repoRef, err),
			"Access denied to the container registry. The image may require authentication.")
	}
	if strings.Contains(errStr, "NAME_UNKNOWN") {
		return NewCompareError("registry-list",
			fmt.Errorf("repository not found: %s", repoRef),
			"The RDS repository does not exist for this RHEL version")
	}
	return NewCompareError("registry-list",
		fmt.Errorf("failed to list tags from %s: %w", repoRef, err),
		"Could not connect to the container registry. Verify network connectivity.")
}

// ResolveRDSArgs holds the parsed arguments for the kube_compare_resolve_rds operation.
type ResolveRDSArgs struct {
	Kubeconfig string
	Context    string
	RDSType    string
	OCPVersion string // Optional: explicit OpenShift version
}

// ExtractMajorMinorVersion extracts the major.minor version from a full version string.
func ExtractMajorMinorVersion(version string) string {
	matches := majorMinorVersionRegex.FindStringSubmatch(version)
	if len(matches) >= 3 {
		return fmt.Sprintf("v%s.%s", matches[1], matches[2])
	}
	// Fallback: return as-is with v prefix
	return "v" + version
}

// BuildRDSReference constructs the container reference string for an RDS type.
func BuildRDSReference(rdsType, rhelVariant, ocpVersion string) string {
	cfg := rdsConfigs[rdsType]
	// Build image reference with RHEL variant: e.g., openshift-telco-core-rds-rhel9:v4.18
	imageRef := fmt.Sprintf("%s-%s:%s", cfg.ImageBase, rhelVariant, ocpVersion)
	return fmt.Sprintf("container://%s:%s", imageRef, cfg.Path)
}

// FilterVersionTags filters a list of tags to only include version tags.
func FilterVersionTags(tags []string) []string {
	versionTags := []string{}

	for _, tag := range tags {
		if versionTagRegex.MatchString(tag) {
			versionTags = append(versionTags, tag)
		}
	}

	sort.Slice(versionTags, func(i, j int) bool {
		return CompareVersionTags(versionTags[i], versionTags[j]) < 0
	})

	return versionTags
}

// CompareVersionTags compares two version tags (e.g., "v4.18" vs "v4.20").
// Returns negative if a < b, zero if a == b, positive if a > b.
func CompareVersionTags(a, b string) int {
	// Parse version numbers
	var aMajor, aMinor, bMajor, bMinor int
	_, _ = fmt.Sscanf(a, "v%d.%d", &aMajor, &aMinor)
	_, _ = fmt.Sscanf(b, "v%d.%d", &bMajor, &bMinor)

	if aMajor != bMajor {
		return aMajor - bMajor
	}
	return aMinor - bMinor
}

// ContainsTag checks if a specific tag exists in a list of tags.
func ContainsTag(tags []string, target string) bool {
	for _, tag := range tags {
		if tag == target {
			return true
		}
	}
	return false
}
