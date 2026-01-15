// SPDX-License-Identifier: Apache-2.0

//go:generate mockgen -destination=mock_interfaces_test.go -package=mcpserver_test github.com/sakhoury/kube-compare-mcp/pkg/mcpserver RegistryClient,ClusterClient,ClusterClientFactory,HTTPDoer

package mcpserver

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// RegistryClient abstracts OCI registry operations for testing.
type RegistryClient interface {
	// ListTags returns available tags for a repository.
	ListTags(ctx context.Context, repo string) ([]string, error)
	// HeadImage performs a HEAD request on an image to validate it exists.
	HeadImage(ctx context.Context, imageRef string) error
}

// ClusterClient abstracts Kubernetes cluster operations for testing.
type ClusterClient interface {
	// GetClusterVersion returns the OpenShift cluster version from the ClusterVersion resource.
	GetClusterVersion(ctx context.Context) (string, error)
}

// ClusterClientFactory creates ClusterClient instances from rest.Config.
type ClusterClientFactory interface {
	// NewClient creates a new ClusterClient from the given rest.Config.
	NewClient(config *rest.Config) (ClusterClient, error)
}

// HTTPDoer abstracts HTTP client operations for testing.
type HTTPDoer interface {
	// Do performs an HTTP request.
	Do(req *http.Request) (*http.Response, error)
}

// DefaultRegistryClient is the production implementation of RegistryClient.
type DefaultRegistryClient struct{}

// ListTags lists all available tags from a container image repository.
func (c *DefaultRegistryClient) ListTags(ctx context.Context, repoRef string) ([]string, error) {
	repo, err := name.NewRepository(repoRef)
	if err != nil {
		return nil, fmt.Errorf("invalid repository reference %q: %w", repoRef, err)
	}

	tags, err := remote.List(repo,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list tags for %q: %w", repoRef, err)
	}
	return tags, nil
}

// HeadImage performs a HEAD request on an image to validate it exists and is accessible.
func (c *DefaultRegistryClient) HeadImage(ctx context.Context, imageRef string) error {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return fmt.Errorf("invalid image reference %q: %w", imageRef, err)
	}

	_, err = remote.Head(ref,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	)
	if err != nil {
		return fmt.Errorf("failed to access image %q: %w", imageRef, err)
	}
	return nil
}

// DefaultClusterClient is the production implementation of ClusterClient.
type DefaultClusterClient struct {
	client dynamic.Interface
}

// GetClusterVersion queries the cluster for its OpenShift version.
func (c *DefaultClusterClient) GetClusterVersion(ctx context.Context) (string, error) {
	clusterVersionGVR := schema.GroupVersionResource{
		Group:    "config.openshift.io",
		Version:  "v1",
		Resource: "clusterversions",
	}

	result, err := c.client.Resource(clusterVersionGVR).Get(ctx, "version", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get ClusterVersion: %w", err)
	}

	version, found, err := unstructured.NestedString(result.Object, "status", "desired", "version")
	if err != nil {
		return "", fmt.Errorf("failed to extract version from ClusterVersion: %w", err)
	}
	if !found {
		return "", fmt.Errorf("version not found in ClusterVersion status")
	}

	return version, nil
}

// DefaultClusterClientFactory is the production implementation of ClusterClientFactory.
type DefaultClusterClientFactory struct{}

// NewClient creates a new DefaultClusterClient from the given rest.Config.
func (f *DefaultClusterClientFactory) NewClient(config *rest.Config) (ClusterClient, error) {
	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}
	return &DefaultClusterClient{client: dynClient}, nil
}

// DefaultHTTPDoer is the production implementation of HTTPDoer using http.Client.
type DefaultHTTPDoer struct {
	Client *http.Client
}

// Do performs an HTTP request using the underlying http.Client.
func (d *DefaultHTTPDoer) Do(req *http.Request) (*http.Response, error) {
	resp, err := d.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	return resp, nil
}

// Package-level default implementations for production use.
// These can be overridden in tests.
var (
	// DefaultRegistry is the default RegistryClient implementation.
	DefaultRegistry RegistryClient = &DefaultRegistryClient{}

	// DefaultClusterFactory is the default ClusterClientFactory implementation.
	DefaultClusterFactory ClusterClientFactory = &DefaultClusterClientFactory{}
)
