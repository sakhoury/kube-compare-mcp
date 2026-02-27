// SPDX-License-Identifier: Apache-2.0

package acm

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
)

// ResourceInspector abstracts Kubernetes cluster inspection operations for testing.
type ResourceInspector interface {
	GetResource(ctx context.Context, gvr schema.GroupVersionResource, name, namespace string) (*unstructured.Unstructured, error)
	ListResources(ctx context.Context, gvr schema.GroupVersionResource, namespace string) ([]unstructured.Unstructured, error)
	DryRunPatch(ctx context.Context, gvr schema.GroupVersionResource, name, namespace string, patch []byte) error
	CheckAccess(ctx context.Context, verb string, gvr schema.GroupVersionResource, namespace string) (bool, error)
}

// ACMClientFactory creates ResourceInspector instances from a rest.Config.
type ACMClientFactory interface {
	NewResourceInspector(config *rest.Config) (ResourceInspector, error)
}
