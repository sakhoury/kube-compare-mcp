// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const (
	maxKubeconfigSize        = 1 * 1024 * 1024
	maxDecodedKubeconfigSize = 768 * 1024
)

// DecodeKubeconfig decodes a base64-encoded kubeconfig string with size limits.
func DecodeKubeconfig(base64Input string) ([]byte, error) {
	logger := slog.Default()

	// Trim whitespace (including newlines) that may be present when content is pasted
	base64Input = strings.TrimSpace(base64Input)

	if base64Input == "" {
		return nil, NewValidationError("kubeconfig",
			"kubeconfig is empty after trimming whitespace",
			"Provide the base64-encoded kubeconfig content, not an empty value")
	}

	if len(base64Input) > maxKubeconfigSize {
		logger.Warn("Kubeconfig size exceeds limit",
			"size", len(base64Input),
			"maxSize", maxKubeconfigSize,
		)
		return nil, NewSecurityError("kubeconfig-size-limit",
			fmt.Sprintf("kubeconfig size (%d bytes) exceeds maximum allowed (%d bytes)", len(base64Input), maxKubeconfigSize),
			"Reduce the kubeconfig size by removing unused contexts, clusters, or users")
	}

	decoded, err := base64.StdEncoding.DecodeString(base64Input)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(base64Input)
		if err != nil {
			// Log helpful debug info (size only, not content for security)
			logger.Debug("Failed to decode kubeconfig base64",
				"inputLength", len(base64Input),
				"firstChars", safePrefix(base64Input, 10),
				"lastChars", safeSuffix(base64Input, 10),
			)
			return nil, NewValidationError("kubeconfig",
				fmt.Sprintf("invalid base64 encoding (input length: %d bytes)", len(base64Input)),
				"Ensure the kubeconfig is properly base64 encoded without truncation or modification")
		}
	}

	if len(decoded) > maxDecodedKubeconfigSize {
		return nil, NewSecurityError("kubeconfig-size-limit",
			fmt.Sprintf("decoded kubeconfig size (%d bytes) exceeds maximum allowed (%d bytes)", len(decoded), maxDecodedKubeconfigSize),
			"Reduce the kubeconfig size by removing unused contexts, clusters, or users")
	}

	logger.Debug("Kubeconfig decoded successfully", "decodedSize", len(decoded))
	return decoded, nil
}

// DecodeOrParseKubeconfig auto-detects whether the input is raw YAML or base64-encoded
// kubeconfig content. It returns the raw YAML bytes for further processing.
// If input is empty, it returns nil to signal that in-cluster config should be used.
// Detection order:
//  1. If empty/not provided: return nil (signals in-cluster config)
//  2. Try parsing as YAML using clientcmd.Load() - if valid kubeconfig, use directly
//  3. Try base64 decoding - if successful and decodes to valid YAML, use that
//  4. If both fail, return a helpful error message
func DecodeOrParseKubeconfig(input string) ([]byte, error) {
	logger := slog.Default()

	// Trim whitespace (including newlines) that may be present when content is pasted
	input = strings.TrimSpace(input)

	// If empty, signal that in-cluster config should be used
	if input == "" {
		logger.Debug("No kubeconfig provided, signaling in-cluster config")
		return nil, nil
	}

	// Check size limits first
	if len(input) > maxKubeconfigSize {
		logger.Warn("Kubeconfig size exceeds limit",
			"size", len(input),
			"maxSize", maxKubeconfigSize,
		)
		return nil, NewSecurityError("kubeconfig-size-limit",
			fmt.Sprintf("kubeconfig size (%d bytes) exceeds maximum allowed (%d bytes)", len(input), maxKubeconfigSize),
			"Reduce the kubeconfig size by removing unused contexts, clusters, or users")
	}

	// Try parsing as raw YAML first
	// A valid kubeconfig YAML will have 'apiVersion' and parse successfully
	config, yamlErr := clientcmd.Load([]byte(input))
	if yamlErr == nil && config != nil && len(config.Clusters) > 0 {
		// Valid kubeconfig YAML
		logger.Debug("Detected raw YAML kubeconfig format", "size", len(input))

		if len(input) > maxDecodedKubeconfigSize {
			return nil, NewSecurityError("kubeconfig-size-limit",
				fmt.Sprintf("kubeconfig size (%d bytes) exceeds maximum allowed (%d bytes)", len(input), maxDecodedKubeconfigSize),
				"Reduce the kubeconfig size by removing unused contexts, clusters, or users")
		}

		return []byte(input), nil
	}

	// Try base64 decoding
	decoded, err := base64.StdEncoding.DecodeString(input)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(input)
	}

	if err == nil {
		// Successfully decoded as base64, verify it's valid kubeconfig YAML
		config, yamlErr := clientcmd.Load(decoded)
		if yamlErr == nil && config != nil && len(config.Clusters) > 0 {
			logger.Debug("Detected base64-encoded kubeconfig format", "decodedSize", len(decoded))

			if len(decoded) > maxDecodedKubeconfigSize {
				return nil, NewSecurityError("kubeconfig-size-limit",
					fmt.Sprintf("decoded kubeconfig size (%d bytes) exceeds maximum allowed (%d bytes)", len(decoded), maxDecodedKubeconfigSize),
					"Reduce the kubeconfig size by removing unused contexts, clusters, or users")
			}

			return decoded, nil
		}
	}

	// Both attempts failed, provide a helpful error message
	logger.Debug("Failed to detect kubeconfig format",
		"inputLength", len(input),
		"firstChars", safePrefix(input, 20),
	)

	return nil, NewValidationError("kubeconfig",
		"unable to parse kubeconfig: not valid YAML or base64-encoded content",
		"Provide either raw kubeconfig YAML content or base64-encoded kubeconfig")
}

// ParseKubeconfig parses kubeconfig bytes into a clientcmd Config structure.
func ParseKubeconfig(data []byte) (*clientcmdapi.Config, error) {
	logger := slog.Default()

	config, err := clientcmd.Load(data)
	if err != nil {
		logger.Debug("Failed to parse kubeconfig", "error", err)
		return nil, NewValidationError("kubeconfig",
			fmt.Sprintf("failed to parse kubeconfig: %v", SanitizeErrorMessage(err.Error())),
			"Verify the kubeconfig is valid YAML and follows the Kubernetes configuration format")
	}

	if len(config.Clusters) == 0 {
		return nil, NewValidationError("kubeconfig",
			"kubeconfig contains no clusters",
			"Add at least one cluster configuration")
	}

	if len(config.AuthInfos) == 0 {
		return nil, NewValidationError("kubeconfig",
			"kubeconfig contains no user credentials",
			"Add at least one user configuration with authentication details")
	}

	if len(config.Contexts) == 0 {
		return nil, NewValidationError("kubeconfig",
			"kubeconfig contains no contexts",
			"Add at least one context that references a cluster and user")
	}

	logger.Debug("Kubeconfig parsed successfully",
		"clusters", len(config.Clusters),
		"users", len(config.AuthInfos),
		"contexts", len(config.Contexts),
		"currentContext", config.CurrentContext,
	)

	return config, nil
}

// ValidateKubeconfigSecurity performs security validation on the kubeconfig.
func ValidateKubeconfigSecurity(config *clientcmdapi.Config) error {
	if err := BlockExecAuth(config); err != nil {
		return err
	}
	if err := BlockAuthProviderPlugins(config); err != nil {
		return err
	}
	return nil
}

// BlockExecAuth checks for and rejects kubeconfigs that use exec-based authentication.
// Exec auth allows arbitrary binary execution, which is a security risk when accepting
// kubeconfigs from untrusted sources.
func BlockExecAuth(config *clientcmdapi.Config) error {
	logger := slog.Default()

	for name, authInfo := range config.AuthInfos {
		if authInfo.Exec != nil {
			logger.Error("Security violation: exec auth blocked",
				"event", "security_violation",
				"violation_type", "exec_auth_blocked",
				"user", name,
				"command", authInfo.Exec.Command,
			)
			return NewSecurityError("exec-auth-blocked",
				fmt.Sprintf("exec-based authentication in user '%s' is not allowed for security reasons", name),
				"Use token, client certificate, or OIDC authentication instead of exec-based auth")
		}
	}

	return nil
}

// BlockAuthProviderPlugins checks for and rejects deprecated auth provider plugins.
// These plugins can execute arbitrary code and are deprecated in favor of exec auth.
func BlockAuthProviderPlugins(config *clientcmdapi.Config) error {
	logger := slog.Default()

	for name, authInfo := range config.AuthInfos {
		if authInfo.AuthProvider != nil {
			logger.Error("Security violation: auth provider blocked",
				"event", "security_violation",
				"violation_type", "auth_provider_blocked",
				"user", name,
				"provider", authInfo.AuthProvider.Name,
			)
			return NewSecurityError("auth-provider-blocked",
				fmt.Sprintf("auth provider plugin '%s' in user '%s' is not allowed for security reasons",
					authInfo.AuthProvider.Name, name),
				"Use token, client certificate, or OIDC authentication instead of auth provider plugins")
		}
	}

	return nil
}

// BuildRestConfig creates a rest.Config from the validated kubeconfig.
func BuildRestConfig(config *clientcmdapi.Config, contextName string) (*rest.Config, error) {
	logger := slog.Default()

	targetContext := contextName
	if targetContext == "" {
		targetContext = config.CurrentContext
	}

	if targetContext == "" {
		return nil, NewValidationError("context",
			"no context specified and kubeconfig has no current-context",
			"Specify a context name or set current-context in the kubeconfig")
	}

	ctx, exists := config.Contexts[targetContext]
	if !exists {
		availableContexts := make([]string, 0, len(config.Contexts))
		for name := range config.Contexts {
			availableContexts = append(availableContexts, name)
		}
		return nil, NewValidationError("context",
			fmt.Sprintf("context '%s' not found in kubeconfig", targetContext),
			fmt.Sprintf("Available contexts: %s", strings.Join(availableContexts, ", ")))
	}

	if _, exists := config.Clusters[ctx.Cluster]; !exists {
		availableClusters := make([]string, 0, len(config.Clusters))
		for name := range config.Clusters {
			availableClusters = append(availableClusters, name)
		}
		return nil, NewValidationError("kubeconfig",
			fmt.Sprintf("cluster '%s' referenced by context '%s' not found", ctx.Cluster, targetContext),
			fmt.Sprintf("Available clusters: %s", strings.Join(availableClusters, ", ")))
	}

	if _, exists := config.AuthInfos[ctx.AuthInfo]; !exists {
		availableUsers := make([]string, 0, len(config.AuthInfos))
		for name := range config.AuthInfos {
			availableUsers = append(availableUsers, name)
		}
		return nil, NewValidationError("kubeconfig",
			fmt.Sprintf("user '%s' referenced by context '%s' not found", ctx.AuthInfo, targetContext),
			fmt.Sprintf("Available users: %s", strings.Join(availableUsers, ", ")))
	}

	logger.Debug("Building REST config",
		"context", targetContext,
		"cluster", ctx.Cluster,
		"user", ctx.AuthInfo,
	)

	clientConfig := clientcmd.NewNonInteractiveClientConfig(
		*config,
		targetContext,
		&clientcmd.ConfigOverrides{},
		nil,
	)

	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, NewCompareError("kubeconfig",
			fmt.Errorf("failed to build client config: %w", err),
			"Verify the kubeconfig authentication and cluster settings are correct")
	}

	logger.Info("Kubeconfig configured for remote cluster",
		"context", targetContext,
		"host", restConfig.Host,
	)

	return restConfig, nil
}

// BuildSecureRestConfig is the main entry point for securely building a REST config
// from a base64-encoded kubeconfig. It performs all validation and security checks.
func BuildSecureRestConfig(base64Kubeconfig, contextName string) (*rest.Config, error) {
	logger := slog.Default()
	logger.Debug("Processing kubeconfig for secure REST config")

	data, err := DecodeKubeconfig(base64Kubeconfig)
	if err != nil {
		return nil, err
	}

	config, err := ParseKubeconfig(data)
	if err != nil {
		return nil, err
	}

	if err := ValidateKubeconfigSecurity(config); err != nil {
		return nil, err
	}

	restConfig, err := BuildRestConfig(config, contextName)
	if err != nil {
		return nil, err
	}

	logger.Debug("Secure REST config built successfully")
	return restConfig, nil
}

// BuildSecureRestConfigFromBytes builds a REST config from raw kubeconfig YAML bytes.
// It performs all validation and security checks.
func BuildSecureRestConfigFromBytes(kubeconfigData []byte, contextName string) (*rest.Config, error) {
	logger := slog.Default()
	logger.Debug("Processing kubeconfig bytes for secure REST config")

	config, err := ParseKubeconfig(kubeconfigData)
	if err != nil {
		return nil, err
	}

	if err := ValidateKubeconfigSecurity(config); err != nil {
		return nil, err
	}

	restConfig, err := BuildRestConfig(config, contextName)
	if err != nil {
		return nil, err
	}

	logger.Debug("Secure REST config built successfully from bytes")
	return restConfig, nil
}

// SanitizeErrorMessage removes potentially sensitive information from error messages.
func SanitizeErrorMessage(msg string) string {
	sensitivePatterns := []string{
		"token",
		"password",
		"secret",
		"credential",
		"bearer",
	}

	lowerMsg := strings.ToLower(msg)
	for _, pattern := range sensitivePatterns {
		if strings.Contains(lowerMsg, pattern) {
			return "configuration error (details redacted for security)"
		}
	}

	return msg
}

// safePrefix returns the first n characters of a string, or the full string if shorter.
// Used for debug logging without exposing full content.
func safePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// safeSuffix returns the last n characters of a string, or the full string if shorter.
// Used for debug logging without exposing full content.
func safeSuffix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
