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

// ResolveHubKubeconfig returns a rest.Config for the hub cluster using in-cluster config.
func ResolveHubKubeconfig(logger *slog.Logger) (*rest.Config, error) {
	logger.Debug("Resolving hub kubeconfig via in-cluster config")
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, NewCompareError("cluster-config",
			fmt.Errorf("failed to get in-cluster config: %w", err),
			"Ensure the server is running inside a Kubernetes cluster with a valid service account.")
	}
	return cfg, nil
}

// DecodeKubeconfig decodes a base64-encoded kubeconfig string with size limits.
func DecodeKubeconfig(base64Input string) ([]byte, error) {
	logger := slog.Default()
	base64Input = strings.TrimSpace(base64Input)

	if base64Input == "" {
		return nil, NewValidationError("kubeconfig",
			"kubeconfig is empty after trimming whitespace",
			"Provide the base64-encoded kubeconfig content, not an empty value")
	}
	if len(base64Input) > maxKubeconfigSize {
		return nil, NewSecurityError("kubeconfig-size-limit",
			fmt.Sprintf("kubeconfig size (%d bytes) exceeds maximum allowed (%d bytes)", len(base64Input), maxKubeconfigSize),
			"Reduce the kubeconfig size by removing unused contexts, clusters, or users")
	}

	decoded, err := base64.StdEncoding.DecodeString(base64Input)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(base64Input)
		if err != nil {
			logger.Debug("Failed to decode kubeconfig base64", "inputLength", len(base64Input))
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
func DecodeOrParseKubeconfig(input string) ([]byte, error) {
	logger := slog.Default()

	input = strings.TrimSpace(input)

	if input == "" {
		logger.Debug("No kubeconfig provided, signaling in-cluster config")
		return nil, nil
	}

	if len(input) > maxKubeconfigSize {
		return nil, NewSecurityError("kubeconfig-size-limit",
			fmt.Sprintf("kubeconfig size (%d bytes) exceeds maximum allowed (%d bytes)", len(input), maxKubeconfigSize),
			"Reduce the kubeconfig size by removing unused contexts, clusters, or users")
	}

	config, yamlErr := clientcmd.Load([]byte(input))
	if yamlErr == nil && config != nil && len(config.Clusters) > 0 {
		logger.Debug("Detected raw YAML kubeconfig format", "size", len(input))
		if len(input) > maxDecodedKubeconfigSize {
			return nil, NewSecurityError("kubeconfig-size-limit",
				fmt.Sprintf("kubeconfig size (%d bytes) exceeds maximum allowed (%d bytes)", len(input), maxDecodedKubeconfigSize),
				"Reduce the kubeconfig size by removing unused contexts, clusters, or users")
		}
		return []byte(input), nil
	}

	decoded, err := base64.StdEncoding.DecodeString(input)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(input)
	}

	if err == nil {
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

	return nil, NewValidationError("kubeconfig",
		"unable to parse kubeconfig: not valid YAML or base64-encoded content",
		"Provide either raw kubeconfig YAML content or base64-encoded kubeconfig")
}

// ParseKubeconfig parses kubeconfig bytes into a clientcmd Config structure.
func ParseKubeconfig(data []byte) (*clientcmdapi.Config, error) {
	logger := slog.Default()

	config, err := clientcmd.Load(data)
	if err != nil {
		return nil, NewValidationError("kubeconfig",
			fmt.Sprintf("failed to parse kubeconfig: %v", SanitizeErrorMessage(err.Error())),
			"Verify the kubeconfig is valid YAML and follows the Kubernetes configuration format")
	}

	if len(config.Clusters) == 0 {
		return nil, NewValidationError("kubeconfig", "kubeconfig contains no clusters",
			"Add at least one cluster configuration")
	}
	if len(config.AuthInfos) == 0 {
		return nil, NewValidationError("kubeconfig", "kubeconfig contains no user credentials",
			"Add at least one user configuration with authentication details")
	}
	if len(config.Contexts) == 0 {
		return nil, NewValidationError("kubeconfig", "kubeconfig contains no contexts",
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
	return BlockAuthProviderPlugins(config)
}

// BlockExecAuth rejects kubeconfigs that use exec-based authentication.
func BlockExecAuth(config *clientcmdapi.Config) error {
	for name, authInfo := range config.AuthInfos {
		if authInfo.Exec != nil {
			return NewSecurityError("exec-auth-blocked",
				fmt.Sprintf("exec-based authentication in user '%s' is not allowed for security reasons", name),
				"Use token, client certificate, or OIDC authentication instead of exec-based auth")
		}
	}
	return nil
}

// BlockAuthProviderPlugins rejects deprecated auth provider plugins.
func BlockAuthProviderPlugins(config *clientcmdapi.Config) error {
	for name, authInfo := range config.AuthInfos {
		if authInfo.AuthProvider != nil {
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
		return nil, NewValidationError("kubeconfig",
			fmt.Sprintf("cluster '%s' referenced by context '%s' not found", ctx.Cluster, targetContext),
			"Check the kubeconfig cluster references")
	}

	if _, exists := config.AuthInfos[ctx.AuthInfo]; !exists {
		return nil, NewValidationError("kubeconfig",
			fmt.Sprintf("user '%s' referenced by context '%s' not found", ctx.AuthInfo, targetContext),
			"Check the kubeconfig user references")
	}

	logger.Debug("Building REST config", "context", targetContext, "cluster", ctx.Cluster, "user", ctx.AuthInfo)

	clientConfig := clientcmd.NewNonInteractiveClientConfig(*config, targetContext, &clientcmd.ConfigOverrides{}, nil)
	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, NewCompareError("kubeconfig",
			fmt.Errorf("failed to build client config: %w", err),
			"Verify the kubeconfig authentication and cluster settings are correct")
	}

	logger.Info("Kubeconfig configured for remote cluster", "context", targetContext, "host", restConfig.Host)
	return restConfig, nil
}

// BuildSecureRestConfig is the entry point for securely building a REST config
// from a base64-encoded kubeconfig.
func BuildSecureRestConfig(base64Kubeconfig, contextName string) (*rest.Config, error) {
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
	return BuildRestConfig(config, contextName)
}

// BuildSecureRestConfigFromBytes builds a REST config from raw kubeconfig YAML bytes.
func BuildSecureRestConfigFromBytes(kubeconfigData []byte, contextName string) (*rest.Config, error) {
	config, err := ParseKubeconfig(kubeconfigData)
	if err != nil {
		return nil, err
	}
	if err := ValidateKubeconfigSecurity(config); err != nil {
		return nil, err
	}
	return BuildRestConfig(config, contextName)
}

// SanitizeErrorMessage removes potentially sensitive information from error messages.
func SanitizeErrorMessage(msg string) string {
	sensitivePatterns := []string{"token", "password", "secret", "credential", "bearer"}
	lowerMsg := strings.ToLower(msg)
	for _, pattern := range sensitivePatterns {
		if strings.Contains(lowerMsg, pattern) {
			return "configuration error (details redacted for security)"
		}
	}
	return msg
}
