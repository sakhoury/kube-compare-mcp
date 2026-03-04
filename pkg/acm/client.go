// SPDX-License-Identifier: Apache-2.0

package acm

import (
	"context"
	"fmt"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// DefaultACMClientFactory is the production implementation of ACMClientFactory.
type DefaultACMClientFactory struct{}

// NewResourceInspector creates a DefaultResourceInspector from the given rest.Config.
func (f *DefaultACMClientFactory) NewResourceInspector(config *rest.Config) (ResourceInspector, error) {
	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}
	return &DefaultResourceInspector{dynClient: dynClient, kubeClient: kubeClient}, nil
}

// DefaultACMFactory is the package-level default factory.
var DefaultACMFactory ACMClientFactory = &DefaultACMClientFactory{}

// DefaultResourceInspector is the production implementation of ResourceInspector.
type DefaultResourceInspector struct {
	dynClient  dynamic.Interface
	kubeClient kubernetes.Interface
}

// GetResource fetches a single resource by GVR, name, and namespace.
func (i *DefaultResourceInspector) GetResource(ctx context.Context, gvr schema.GroupVersionResource, name, namespace string) (*unstructured.Unstructured, error) {
	var res *unstructured.Unstructured
	var err error
	if namespace != "" {
		res, err = i.dynClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	} else {
		res, err = i.dynClient.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("get %s/%s in %q: %w", gvr.Resource, name, namespace, err)
	}
	return res, nil
}

// ListResources lists all resources matching the GVR in the given namespace.
func (i *DefaultResourceInspector) ListResources(ctx context.Context, gvr schema.GroupVersionResource, namespace string) ([]unstructured.Unstructured, error) {
	var list *unstructured.UnstructuredList
	var err error

	if namespace != "" {
		list, err = i.dynClient.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	} else {
		list, err = i.dynClient.Resource(gvr).List(ctx, metav1.ListOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("list %s in %q: %w", gvr.Resource, namespace, err)
	}
	return list.Items, nil
}

// DryRunPatch performs a server-side dry-run merge patch.
func (i *DefaultResourceInspector) DryRunPatch(ctx context.Context, gvr schema.GroupVersionResource, name, namespace string, patch []byte) error {
	var err error
	if namespace != "" {
		_, err = i.dynClient.Resource(gvr).Namespace(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{
			DryRun: []string{metav1.DryRunAll},
		})
	} else {
		_, err = i.dynClient.Resource(gvr).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{
			DryRun: []string{metav1.DryRunAll},
		})
	}
	if err != nil {
		return fmt.Errorf("dry-run patch %s/%s in %q: %w", gvr.Resource, name, namespace, err)
	}
	return nil
}

// CheckAccess checks if the current identity has a specific RBAC permission.
func (i *DefaultResourceInspector) CheckAccess(ctx context.Context, verb string, gvr schema.GroupVersionResource, namespace string) (bool, error) {
	review := &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: namespace,
				Verb:      verb,
				Group:     gvr.Group,
				Version:   gvr.Version,
				Resource:  gvr.Resource,
			},
		},
	}

	result, err := i.kubeClient.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("check access for %s %s/%s: %w", verb, gvr.Group, gvr.Resource, err)
	}

	return result.Status.Allowed, nil
}
