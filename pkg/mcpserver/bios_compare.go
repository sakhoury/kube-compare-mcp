// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"github.com/adrg/strutil"
	"github.com/adrg/strutil/metrics"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	sigsyaml "sigs.k8s.io/yaml"
)

const (
	// DefaultReferenceConfigNamespace is the default namespace for BIOS reference ConfigMaps.
	DefaultReferenceConfigNamespace = "reference-configs"

	// BMHRoleAnnotation is the annotation key for node role on BareMetalHost.
	BMHRoleAnnotation = "bmac.agent-install.openshift.io/role"

	// minModelSimilarity is the minimum Smith-Waterman-Gotoh similarity score
	// (0.0-1.0) required to accept a reference  BIOS ConfigMap model match.
	// Values below this threshold indicate the product name and label are too dissimilar.
	minModelSimilarity = 0.7
)

// GVRs for metal3 and related resources
var (
	bareMetalHostGVR = schema.GroupVersionResource{
		Group:    "metal3.io",
		Version:  "v1alpha1",
		Resource: "baremetalhosts",
	}

	hardwareDataGVR = schema.GroupVersionResource{
		Group:    "metal3.io",
		Version:  "v1alpha1",
		Resource: "hardwaredata",
	}

	hostFirmwareComponentsGVR = schema.GroupVersionResource{
		Group:    "metal3.io",
		Version:  "v1alpha1",
		Resource: "hostfirmwarecomponents",
	}

	hostFirmwareSettingsGVR = schema.GroupVersionResource{
		Group:    "metal3.io",
		Version:  "v1alpha1",
		Resource: "hostfirmwaresettings",
	}

	configMapGVR = schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "configmaps",
	}
)

// BIOSDiffInput defines the typed input for the baremetal_bios_diff tool.
// Field descriptions are optimized for AI assistant consumption.
type BIOSDiffInput struct {
	Kubeconfig        string `json:"kubeconfig,omitempty" jsonschema:"Kubeconfig content (raw YAML or base64-encoded) for the ACM hub cluster. If omitted, uses in-cluster config."`
	Context           string `json:"context,omitempty" jsonschema:"Kubernetes context name to use from the provided kubeconfig."`
	Namespace         string `json:"namespace" jsonschema:"Namespace on the hub cluster containing BareMetalHost resources to compare."`
	HostName          string `json:"host_name,omitempty" jsonschema:"Specific host to compare. Omit to compare all hosts in the namespace."`
	ReferenceSource   string `json:"reference_source,omitempty" jsonschema:"Namespace containing BIOS reference ConfigMaps."`
	ReferenceOverride string `json:"reference_override,omitempty" jsonschema:"Explicit ConfigMap name to use, bypassing auto-matching by server model."`
	OutputFormat      string `json:"output_format,omitempty" jsonschema:"Output format for results."`
}

// BIOSDiffOutput is an empty output struct (tool returns text content).
type BIOSDiffOutput struct{}

// BIOSDiffResult is the structured response for the baremetal_bios_diff tool.
// Output format aligns with kube-compare conventions (PascalCase JSON keys).
type BIOSDiffResult struct {
	Namespace string           `json:"Namespace"`
	Hosts     []HostBIOSResult `json:"Hosts"`
	Summary   BIOSDiffSummary  `json:"Summary"`
}

// HostBIOSResult contains the BIOS comparison result for a single host.
type HostBIOSResult struct {
	Name            string            `json:"Name"`
	Namespace       string            `json:"Namespace"`
	Role            string            `json:"Role"`
	ServerModel     ServerModelInfo   `json:"ServerModel"`
	Reference       string            `json:"Reference"`
	ReferenceSource string            `json:"ReferenceSource,omitempty"`
	BIOSVersion     BIOSVersionResult `json:"BIOSVersion"`
	SettingsDiff    []BIOSSettingDiff `json:"SettingsDiff,omitempty"`
	Compliant       bool              `json:"Compliant"`
	Error           string            `json:"Error,omitempty"`
}

const (
	// ReferenceSourceMCPServer indicates the reference ConfigMap was found on the MCP server cluster.
	// Reference ConfigMaps are only loaded from the MCP server cluster for security reasons -
	// this ensures the server operator controls the compliance baseline, not the user.
	ReferenceSourceMCPServer = "mcp-server-cluster"
)

// ServerModelInfo contains server hardware identification.
type ServerModelInfo struct {
	Manufacturer string `json:"Manufacturer"`
	ProductName  string `json:"ProductName"`
}

// BIOSVersionResult contains the BIOS version comparison.
type BIOSVersionResult struct {
	Expected string `json:"Expected"`
	Actual   string `json:"Actual"`
	Match    bool   `json:"Match"`
}

// BIOSSettingDiff represents a difference in a BIOS setting.
type BIOSSettingDiff struct {
	Setting  string `json:"Setting"`
	Expected string `json:"Expected"`
	Actual   string `json:"Actual"`
}

// BIOSDiffSummary provides an overview of the comparison results.
// Field naming aligns with kube-compare conventions (e.g., NumDiffHosts ~ NumDiffCRs).
type BIOSDiffSummary struct {
	TotalHosts     int `json:"TotalHosts"`
	CompliantHosts int `json:"CompliantHosts"`
	NumDiffHosts   int `json:"NumDiffHosts"`
	ErrorHosts     int `json:"ErrorHosts"`
}

// BIOSDiffTool returns the MCP tool definition for BIOS comparison.
func BIOSDiffTool() *mcp.Tool {
	return &mcp.Tool{
		Name:         "baremetal_bios_diff",
		Title:        "BIOS Configuration Comparator",
		Description:  "Compare BIOS versions and settings of bare metal hosts against reference configurations. Targets ZTP-provisioned clusters managed via ACM hub.",
		InputSchema:  BIOSDiffInputSchema(),
		OutputSchema: BIOSDiffOutputSchema(),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			DestructiveHint: ptrBool(false),
			IdempotentHint:  true,
			OpenWorldHint:   ptrBool(true),
		},
	}
}

// HandleBIOSDiff is the MCP tool handler for the baremetal_bios_diff tool.
func HandleBIOSDiff(ctx context.Context, req *mcp.CallToolRequest, input BIOSDiffInput) (toolResult *mcp.CallToolResult, biosResult *BIOSDiffResult, toolErr error) {
	requestID := generateRequestID()
	logger := slog.Default().With("requestID", requestID)
	start := time.Now()

	logger.Info("Received tool request",
		"tool", "baremetal_bios_diff",
		"namespace", input.Namespace,
		"hostName", input.HostName,
		"referenceSource", input.ReferenceSource,
		"referenceOverride", input.ReferenceOverride,
		"hasKubeconfig", input.Kubeconfig != "",
		"context", input.Context,
		"outputFormat", input.OutputFormat,
	)

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
		return newToolResultError(formatErrorForUser(ErrContextCanceled)), nil, nil
	}

	// Validate context requires kubeconfig
	if input.Context != "" && input.Kubeconfig == "" {
		err := NewValidationError("context",
			"'context' parameter requires 'kubeconfig' to also be provided",
			"Provide a kubeconfig along with the context name")
		logger.Debug("Validation failed", "error", err)
		return newToolResultError(formatErrorForUser(err)), nil, nil
	}

	// Validate required fields
	if input.Namespace == "" {
		err := NewValidationError("namespace",
			"namespace is required",
			"Provide the namespace on the hub cluster containing the BareMetalHost resources")
		return newToolResultError(formatErrorForUser(err)), nil, nil
	}

	// Set defaults
	referenceSource := input.ReferenceSource
	if referenceSource == "" {
		referenceSource = DefaultReferenceConfigNamespace
	}

	logger.Debug("Parsed baremetal_bios_diff arguments",
		"namespace", input.Namespace,
		"hostName", input.HostName,
		"referenceSource", referenceSource,
		"hasKubeconfig", input.Kubeconfig != "",
		"context", input.Context,
	)

	// Build REST config
	var restConfig *rest.Config
	var err error

	if input.Kubeconfig != "" {
		logger.Debug("Using provided kubeconfig for hub cluster connection",
			"kubeconfigLength", len(input.Kubeconfig),
		)

		kubeconfigData, err := DecodeOrParseKubeconfig(input.Kubeconfig)
		if err != nil {
			logger.Debug("Kubeconfig parsing failed", "error", err)
			return newToolResultError(formatErrorForUser(err)), nil, nil
		}

		restConfig, err = BuildSecureRestConfigFromBytes(kubeconfigData, input.Context)
		if err != nil {
			logger.Debug("Failed to build REST config from kubeconfig", "error", err)
			return newToolResultError(formatErrorForUser(err)), nil, nil
		}
	} else {
		logger.Debug("Using in-cluster config for hub cluster connection")
		restConfig, err = rest.InClusterConfig()
		if err != nil {
			err = NewCompareError("cluster-config",
				fmt.Errorf("failed to get in-cluster config: %w", err),
				"No kubeconfig provided and in-cluster config not available. "+
					"Provide a kubeconfig for the hub cluster.")
			return newToolResultError(formatErrorForUser(err)), nil, nil
		}
	}

	// Create dynamic client for hub cluster (target workload data only)
	targetClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		err = NewCompareError("cluster-client",
			fmt.Errorf("failed to create dynamic client: %w", err),
			"Verify the kubeconfig is valid")
		return newToolResultError(formatErrorForUser(err)), nil, nil
	}

	// Create reference client from in-cluster config (for reference ConfigMaps).
	// Reference ConfigMaps are ONLY loaded from the MCP server cluster for security:
	// the server operator controls the compliance baseline, not the user.
	var referenceClient dynamic.Interface
	inClusterConfig, inClusterErr := rest.InClusterConfig()
	if inClusterErr != nil {
		err = NewCompareError("reference-config",
			fmt.Errorf("in-cluster config not available: %w", inClusterErr),
			"The MCP server must run inside a Kubernetes cluster to access reference ConfigMaps. "+
				"Deploy reference ConfigMaps to the MCP server cluster namespace '"+referenceSource+"'.")
		return newToolResultError(formatErrorForUser(err)), nil, nil
	}
	referenceClient, err = dynamic.NewForConfig(inClusterConfig)
	if err != nil {
		err = NewCompareError("reference-client",
			fmt.Errorf("failed to create reference client: %w", err),
			"Unable to connect to the MCP server cluster for reference ConfigMaps")
		return newToolResultError(formatErrorForUser(err)), nil, nil
	}
	logger.Debug("Reference client created from in-cluster config for secure ConfigMap lookup")

	// Run the comparison
	result, err := runBIOSComparison(ctx, targetClient, referenceClient, input.Namespace, input.HostName, referenceSource, input.ReferenceOverride, logger)
	if err != nil {
		return newToolResultError(formatErrorForUser(err)), nil, nil
	}

	// Format output
	var outputBytes []byte
	switch input.OutputFormat {
	case "yaml":
		outputBytes, err = sigsyaml.Marshal(result)
	case "json", "":
		outputBytes, err = json.MarshalIndent(result, "", "  ")
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to format result: %w", err)
	}

	duration := time.Since(start)
	logger.Info("BIOS comparison completed",
		"duration", duration,
		"namespace", input.Namespace,
		"totalHosts", result.Summary.TotalHosts,
		"compliantHosts", result.Summary.CompliantHosts,
		"numDiffHosts", result.Summary.NumDiffHosts,
	)

	return newToolResultText(string(outputBytes)), result, nil
}

// runBIOSComparison performs the actual BIOS comparison logic.
// targetClient is used for reading workload data (BMH, HardwareData, HostFirmware*) from the hub cluster.
// referenceClient is used for reading reference ConfigMaps from the MCP server cluster.
func runBIOSComparison(
	ctx context.Context,
	targetClient dynamic.Interface,
	referenceClient dynamic.Interface,
	namespace string,
	hostName string,
	referenceSource string,
	referenceOverride string,
	logger *slog.Logger,
) (*BIOSDiffResult, error) {
	// Get BMH resources from target cluster
	var bmhList *unstructured.UnstructuredList
	var err error

	if hostName != "" {
		// Get specific BMH
		bmh, err := targetClient.Resource(bareMetalHostGVR).Namespace(namespace).Get(ctx, hostName, metav1.GetOptions{})
		if err != nil {
			return nil, NewCompareError("get-bmh",
				fmt.Errorf("failed to get BareMetalHost %s/%s: %w", namespace, hostName, err),
				"Verify the host name and namespace are correct")
		}
		bmhList = &unstructured.UnstructuredList{
			Items: []unstructured.Unstructured{*bmh},
		}
	} else {
		// List all BMHs in namespace
		bmhList, err = targetClient.Resource(bareMetalHostGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, NewCompareError("list-bmh",
				fmt.Errorf("failed to list BareMetalHosts in namespace %s: %w", namespace, err),
				"Verify the namespace exists and you have permission to list BareMetalHosts")
		}
	}

	if len(bmhList.Items) == 0 {
		logger.Debug("No BareMetalHosts found", "namespace", namespace, "hostName", hostName)
		return nil, NewCompareError("no-bmh",
			fmt.Errorf("no BareMetalHosts found in namespace %s", namespace),
			"Verify the namespace contains BareMetalHost resources")
	}

	logger.Info("Found BMHs to compare", "count", len(bmhList.Items), "namespace", namespace)

	result := &BIOSDiffResult{
		Namespace: namespace,
		Hosts:     make([]HostBIOSResult, 0, len(bmhList.Items)),
		Summary: BIOSDiffSummary{
			TotalHosts: len(bmhList.Items),
		},
	}

	for _, bmh := range bmhList.Items {
		hostResult := compareBMHBIOS(ctx, targetClient, referenceClient, &bmh, referenceSource, referenceOverride, logger)
		result.Hosts = append(result.Hosts, hostResult)

		switch {
		case hostResult.Error != "":
			result.Summary.ErrorHosts++
		case hostResult.Compliant:
			result.Summary.CompliantHosts++
		default:
			result.Summary.NumDiffHosts++
		}
	}

	return result, nil
}

// compareBMHBIOS compares a single BMH's BIOS against reference.
// targetClient is used for reading workload data from the hub cluster.
// referenceClient is used for reading reference ConfigMaps from the MCP server cluster.
func compareBMHBIOS(
	ctx context.Context,
	targetClient dynamic.Interface,
	referenceClient dynamic.Interface,
	bmh *unstructured.Unstructured,
	refSourceNamespace string,
	refOverride string,
	logger *slog.Logger,
) HostBIOSResult {
	name := bmh.GetName()
	namespace := bmh.GetNamespace()

	result := HostBIOSResult{
		Name:      name,
		Namespace: namespace,
	}

	// Get role from annotation
	annotations := bmh.GetAnnotations()
	role := annotations[BMHRoleAnnotation]
	if role == "" {
		role = "worker" // Default to worker if not specified
		logger.Warn("No role annotation found, defaulting to worker", "bmh", name)
	}
	result.Role = role

	// Get HardwareData for server model from target cluster
	hardwareData, err := targetClient.Resource(hardwareDataGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		result.Error = fmt.Sprintf("failed to get HardwareData: %v", err)
		logger.Debug("Failed to get HardwareData", "bmh", name, "error", err)
		return result
	}

	manufacturer, _, _ := unstructured.NestedString(hardwareData.Object, "spec", "hardware", "systemVendor", "manufacturer")
	productName, _, _ := unstructured.NestedString(hardwareData.Object, "spec", "hardware", "systemVendor", "productName")

	result.ServerModel = ServerModelInfo{
		Manufacturer: manufacturer,
		ProductName:  productName,
	}

	// Get HostFirmwareComponents for BIOS version from target cluster
	firmwareComponents, err := targetClient.Resource(hostFirmwareComponentsGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		result.Error = fmt.Sprintf("failed to get HostFirmwareComponents: %v", err)
		logger.Debug("Failed to get HostFirmwareComponents", "bmh", name, "error", err)
		return result
	}

	actualBIOSVersion := extractBIOSVersion(firmwareComponents)

	// Get HostFirmwareSettings for BIOS settings from target cluster
	firmwareSettings, err := targetClient.Resource(hostFirmwareSettingsGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		result.Error = fmt.Sprintf("failed to get HostFirmwareSettings: %v", err)
		logger.Debug("Failed to get HostFirmwareSettings", "bmh", name, "error", err)
		return result
	}

	actualSettings := extractBIOSSettings(firmwareSettings)

	// Find reference ConfigMap from MCP server cluster only (security: operator controls baseline)
	var refConfigMap *unstructured.Unstructured
	var configMapName string

	refConfigMap, configMapName, err = findReferenceConfigMap(
		ctx, referenceClient, refSourceNamespace, refOverride,
		manufacturer, productName, role, logger,
	)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Reference = configMapName
	result.ReferenceSource = ReferenceSourceMCPServer

	// Extract reference values from ConfigMap
	refData, _, _ := unstructured.NestedStringMap(refConfigMap.Object, "data")
	expectedBIOSVersion := refData["biosVersion"]
	expectedSettings := parseSettingsYAML(refData["settings"])

	// Compare BIOS version
	result.BIOSVersion = BIOSVersionResult{
		Expected: expectedBIOSVersion,
		Actual:   actualBIOSVersion,
		Match:    expectedBIOSVersion == actualBIOSVersion,
	}

	// Compare settings
	result.SettingsDiff = compareBIOSSettings(expectedSettings, actualSettings)

	// Determine compliance
	result.Compliant = result.BIOSVersion.Match && len(result.SettingsDiff) == 0

	logger.Debug("Completed BMH comparison",
		"bmh", name,
		"compliant", result.Compliant,
		"biosVersionMatch", result.BIOSVersion.Match,
		"settingsDiffs", len(result.SettingsDiff),
	)

	return result
}

// extractBIOSVersion extracts the BIOS version from HostFirmwareComponents.
func extractBIOSVersion(hfc *unstructured.Unstructured) string {
	components, found, err := unstructured.NestedSlice(hfc.Object, "status", "components")
	if err != nil || !found {
		return ""
	}

	for _, comp := range components {
		compMap, ok := comp.(map[string]any)
		if !ok {
			continue
		}
		componentName, _, _ := unstructured.NestedString(compMap, "component")
		if componentName == "bios" {
			version, _, _ := unstructured.NestedString(compMap, "currentVersion")
			return version
		}
	}
	return ""
}

// extractBIOSSettings extracts BIOS settings from HostFirmwareSettings.
func extractBIOSSettings(hfs *unstructured.Unstructured) map[string]string {
	settings, found, err := unstructured.NestedStringMap(hfs.Object, "status", "settings")
	if err != nil || !found {
		return make(map[string]string)
	}
	return settings
}

// findReferenceConfigMap finds a reference ConfigMap from the MCP server cluster.
// If explicitConfigMap is set, looks for that specific ConfigMap.
// Otherwise, tries exact name match then label-based best match.
// Reference ConfigMaps are only loaded from the MCP server cluster for security -
// this ensures the server operator controls the compliance baseline, not the user.
func findReferenceConfigMap(
	ctx context.Context,
	referenceClient dynamic.Interface,
	referenceNamespace string,
	explicitConfigMap string,
	manufacturer string,
	productName string,
	role string,
	logger *slog.Logger,
) (*unstructured.Unstructured, string, error) {
	if explicitConfigMap != "" {
		refConfigMap, err := referenceClient.Resource(configMapGVR).Namespace(referenceNamespace).Get(ctx, explicitConfigMap, metav1.GetOptions{})
		if err != nil {
			return nil, "", fmt.Errorf("reference override ConfigMap %q not found in namespace %q: %w", explicitConfigMap, referenceNamespace, err)
		}
		logger.Info("Found reference ConfigMap on MCP server cluster", "configmap", explicitConfigMap, "namespace", referenceNamespace)
		return refConfigMap, explicitConfigMap, nil
	}

	// Auto-match: try exact name match first
	configMapName := buildReferenceConfigMapName(manufacturer, productName, role)
	refConfigMap, err := referenceClient.Resource(configMapGVR).Namespace(referenceNamespace).Get(ctx, configMapName, metav1.GetOptions{})
	if err == nil {
		logger.Info("Found reference ConfigMap on MCP server cluster", "configmap", configMapName, "namespace", referenceNamespace)
		return refConfigMap, configMapName, nil
	}

	// Fall back to label-based best match
	exactMatchName := configMapName
	logger.Debug("Exact ConfigMap match not found, trying label-based match", "tried", exactMatchName)
	refConfigMap, matchedName, err := findBestMatchConfigMap(ctx, referenceClient, referenceNamespace, manufacturer, productName, role, logger)
	if err != nil {
		return nil, "", fmt.Errorf("no matching reference ConfigMap found for vendor=%s role=%s (tried exact: %s) on MCP server cluster: %w",
			manufacturer, role, exactMatchName, err)
	}

	logger.Info("Found reference ConfigMap on MCP server cluster", "configmap", matchedName, "namespace", referenceNamespace)
	return refConfigMap, matchedName, nil
}

// buildReferenceConfigMapName constructs the ConfigMap name from server info.
// Format: bios-ref-<manufacturer>-<model>-<role>
func buildReferenceConfigMapName(manufacturer, productName, role string) string {
	mfr := normalizeForK8sName(manufacturer, 0)
	model := normalizeForK8sName(productName, 0)

	return fmt.Sprintf("bios-ref-%s-%s-%s", mfr, model, role)
}

// findBestMatchConfigMap searches for a ConfigMap matching vendor, role, and model using labels.
// Uses score-based matching to find the best model match.
// Returns the ConfigMap, its name, and any error.
func findBestMatchConfigMap(
	ctx context.Context,
	client dynamic.Interface,
	referenceNamespace string,
	manufacturer string,
	productName string,
	role string,
	logger *slog.Logger,
) (*unstructured.Unstructured, string, error) {
	// Normalize vendor and role for label matching (labels can't contain spaces or special chars)
	vendor := normalizeForK8sName(manufacturer, validation.DNS1123LabelMaxLength)
	normalizedRole := normalizeForK8sName(role, validation.DNS1123LabelMaxLength)

	// List ConfigMaps with matching vendor and role labels
	labelSelector := fmt.Sprintf("bios-reference/vendor=%s,bios-reference/role=%s", vendor, normalizedRole)
	configMaps, err := client.Resource(configMapGVR).Namespace(referenceNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, "", fmt.Errorf("failed to list ConfigMaps with selector %s: %w", labelSelector, err)
	}

	if len(configMaps.Items) == 0 {
		return nil, "", fmt.Errorf("no ConfigMaps found matching vendor=%s role=%s", vendor, role)
	}

	// Score each ConfigMap based on model match and pick the best one
	var bestMatch *unstructured.Unstructured
	var bestName string
	bestScore := -1.0

	for i := range configMaps.Items {
		cm := &configMaps.Items[i]
		labels := cm.GetLabels()
		modelLabel := labels["bios-reference/model"]

		score := scoreModelMatch(productName, modelLabel)
		logger.Debug("Scoring ConfigMap",
			"configmap", cm.GetName(),
			"modelLabel", modelLabel,
			"productName", productName,
			"score", score,
		)

		if score > bestScore {
			bestScore = score
			bestMatch = cm
			bestName = cm.GetName()
		}
	}

	if bestScore < minModelSimilarity {
		return nil, "", fmt.Errorf(
			"no ConfigMap model label is similar enough to %q (best score: %.2f, threshold: %.2f)",
			productName, bestScore, minModelSimilarity,
		)
	}

	logger.Info("Found best matching reference ConfigMap via labels",
		"configmap", bestName,
		"vendor", vendor,
		"role", role,
		"score", bestScore,
		"totalCandidates", len(configMaps.Items),
	)

	return bestMatch, bestName, nil
}

// scoreModelMatch calculates a similarity score between a product name and a model
// label using the Smith-Waterman-Gotoh local alignment algorithm. Returns a float64
// between 0.0 (no similarity) and 1.0 (identical).
func scoreModelMatch(productName, modelLabel string) float64 {
	if modelLabel == "" {
		return 0.0
	}

	swg := metrics.NewSmithWatermanGotoh()
	swg.CaseSensitive = false

	return strutil.Similarity(productName, modelLabel, swg)
}

// normalizeForK8sName converts a string to a valid Kubernetes name component.
// Removes special characters, replaces separators with hyphens, lowercases.
// If maxLen > 0, truncates to that length and trims trailing hyphens.
func normalizeForK8sName(s string, maxLen int) string {
	s = strings.ToLower(s)

	replacer := strings.NewReplacer(
		".", "",
		",", "",
		"(", "",
		")", "",
		"/", " ",
		"-", " ",
		"_", " ",
	)
	s = replacer.Replace(s)

	s = strings.Join(strings.Fields(s), "-")

	if maxLen > 0 && len(s) > maxLen {
		s = s[:maxLen]
		s = strings.TrimRight(s, "-")
	}

	return s
}

// parseSettingsYAML parses the settings YAML string from ConfigMap.
// Format is simple key: value pairs, one per line.
func parseSettingsYAML(settingsStr string) map[string]string {
	settings := make(map[string]string)
	if settingsStr == "" {
		return settings
	}

	lines := strings.Split(settingsStr, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			settings[key] = value
		}
	}

	return settings
}

// compareBIOSSettings compares expected settings against actual settings.
// Only settings specified in the reference are compared.
func compareBIOSSettings(expected, actual map[string]string) []BIOSSettingDiff {
	var diffs []BIOSSettingDiff

	for setting, expectedValue := range expected {
		actualValue, exists := actual[setting]
		if !exists || actualValue != expectedValue {
			diffs = append(diffs, BIOSSettingDiff{
				Setting:  setting,
				Expected: expectedValue,
				Actual:   actualValue,
			})
		}
	}

	return diffs
}
