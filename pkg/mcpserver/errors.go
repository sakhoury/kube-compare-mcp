// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"errors"
	"fmt"
)

// Error types for categorizing different failure modes
var (
	// ErrInvalidArguments indicates the tool was called with invalid parameters
	ErrInvalidArguments = errors.New("invalid arguments")

	// ErrReferenceNotFound indicates the reference configuration file was not found
	ErrReferenceNotFound = errors.New("reference configuration not found")

	// ErrLocalPathNotSupported indicates a local filesystem path was provided
	// but is not supported in remote deployments
	ErrLocalPathNotSupported = errors.New("local filesystem paths are not supported")

	// ErrRemoteUnreachable indicates the remote reference could not be reached
	ErrRemoteUnreachable = errors.New("remote reference unreachable")

	// ErrOCIImageNotFound indicates the container image was not found
	ErrOCIImageNotFound = errors.New("container image not found")

	// ErrClusterConnection indicates a failure to connect to the Kubernetes cluster
	ErrClusterConnection = errors.New("cluster connection failed")

	// ErrComparisonFailed indicates the comparison operation failed
	ErrComparisonFailed = errors.New("comparison failed")

	// ErrContextCanceled indicates the operation was canceled
	ErrContextCanceled = errors.New("operation canceled")

	// ErrSecurityViolation indicates a security policy was violated
	ErrSecurityViolation = errors.New("security policy violation")

	// ErrExecAuthBlocked indicates exec-based auth was blocked for security
	ErrExecAuthBlocked = errors.New("exec-based authentication is not allowed")

	// ErrAuthProviderBlocked indicates auth provider plugins were blocked for security
	ErrAuthProviderBlocked = errors.New("auth provider plugins are not allowed")
)

// CompareError provides detailed error information for comparison failures.
type CompareError struct {
	Op      string // Operation that failed (e.g., "validate", "compare", "initialize")
	Err     error  // Underlying error
	Details string // Additional details or suggestions
}

func (e *CompareError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("%s: %v\n\nDetails: %s", e.Op, e.Err, e.Details)
	}
	return fmt.Sprintf("%s: %v", e.Op, e.Err)
}

func (e *CompareError) Unwrap() error {
	return e.Err
}

// NewCompareError creates a new CompareError with the given operation and error.
func NewCompareError(op string, err error, details string) *CompareError {
	return &CompareError{
		Op:      op,
		Err:     err,
		Details: details,
	}
}

// ValidationError provides detailed error information for argument validation failures.
type ValidationError struct {
	Field   string // Field that failed validation
	Value   string // The invalid value (if safe to include)
	Message string // Description of what's wrong
	Hint    string // Suggestion for fixing the error
}

func (e *ValidationError) Error() string {
	msg := fmt.Sprintf("validation error for '%s': %s", e.Field, e.Message)
	if e.Hint != "" {
		msg += fmt.Sprintf(" (hint: %s)", e.Hint)
	}
	return msg
}

// NewValidationError creates a new ValidationError.
func NewValidationError(field, message, hint string) *ValidationError {
	return &ValidationError{
		Field:   field,
		Message: message,
		Hint:    hint,
	}
}

// SecurityError provides detailed error information for security-related failures.
// This is used when a security policy is violated, such as blocked auth methods.
type SecurityError struct {
	Code    string // Security violation code (e.g., "exec-auth-blocked")
	Message string // Description of the security issue
	Hint    string // Suggestion for resolving the issue
}

func (e *SecurityError) Error() string {
	msg := fmt.Sprintf("security error [%s]: %s", e.Code, e.Message)
	if e.Hint != "" {
		msg += fmt.Sprintf(" (hint: %s)", e.Hint)
	}
	return msg
}

// NewSecurityError creates a new SecurityError.
func NewSecurityError(code, message, hint string) *SecurityError {
	return &SecurityError{
		Code:    code,
		Message: message,
		Hint:    hint,
	}
}

// formatErrorForUser is the internal unexported version for use within the package.
func formatErrorForUser(err error) string {
	return FormatErrorForUser(err)
}

// FormatErrorForUser converts internal errors to user-friendly messages.
func FormatErrorForUser(err error) string {
	if err == nil {
		return ""
	}

	// Check for specific error types
	var compErr *CompareError
	if errors.As(err, &compErr) {
		return compErr.Error()
	}

	var valErr *ValidationError
	if errors.As(err, &valErr) {
		return valErr.Error()
	}

	var secErr *SecurityError
	if errors.As(err, &secErr) {
		return secErr.Error()
	}

	// Check for known error conditions
	if errors.Is(err, ErrReferenceNotFound) {
		return "Reference configuration not found. Please verify the URL is correct and accessible."
	}

	if errors.Is(err, ErrLocalPathNotSupported) {
		return "Local filesystem paths are not supported. " +
			"Please provide a remote reference using HTTP/HTTPS URL or container:// image reference."
	}

	if errors.Is(err, ErrRemoteUnreachable) {
		return "Remote reference is unreachable. " +
			"Please verify the URL is correct and accessible from the cluster."
	}

	if errors.Is(err, ErrOCIImageNotFound) {
		return "Container image not found. " +
			"Please verify the image reference is correct and the image exists in the registry."
	}

	if errors.Is(err, ErrClusterConnection) {
		return "Failed to connect to Kubernetes cluster. " +
			"Please verify the server has access via in-cluster config or KUBECONFIG environment variable."
	}

	if errors.Is(err, ErrContextCanceled) {
		return "Operation was canceled before completion."
	}

	if errors.Is(err, ErrSecurityViolation) {
		return "A security policy was violated. " +
			"Please review the kubeconfig and ensure it does not use exec-based or plugin-based authentication."
	}

	if errors.Is(err, ErrExecAuthBlocked) {
		return "Exec-based authentication is not allowed for security reasons. " +
			"Please use token, client certificate, or OIDC authentication instead."
	}

	if errors.Is(err, ErrAuthProviderBlocked) {
		return "Auth provider plugins are not allowed for security reasons. " +
			"Please use token, client certificate, or OIDC authentication instead."
	}

	// Default: return the error as-is
	return err.Error()
}
