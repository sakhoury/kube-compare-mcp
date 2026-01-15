// SPDX-License-Identifier: Apache-2.0

package mcpserver_test

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"go.uber.org/mock/gomock"

	"github.com/sakhoury/kube-compare-mcp/pkg/mcpserver"
)

// NewFakeClusterVersion creates a fake ClusterVersion unstructured object.
func NewFakeClusterVersion(version string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "config.openshift.io/v1",
			"kind":       "ClusterVersion",
			"metadata": map[string]interface{}{
				"name": "version",
			},
			"status": map[string]interface{}{
				"desired": map[string]interface{}{
					"version": version,
				},
			},
		},
	}
}

// NewFakeDynamicClient creates a fake dynamic client with the given objects.
func NewFakeDynamicClient(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	// Register ClusterVersion GVR
	gvr := schema.GroupVersionResource{
		Group:    "config.openshift.io",
		Version:  "v1",
		Resource: "clusterversions",
	}
	_ = gvr // Just to reference it
	return dynamicfake.NewSimpleDynamicClient(scheme, objects...)
}

// NewTestReferenceService creates a ReferenceService with gomock-generated mocks.
func NewTestReferenceService(ctrl *gomock.Controller) (*mcpserver.ReferenceService, *MockRegistryClient, *MockClusterClient, *MockClusterClientFactory) {
	mockRegistry := NewMockRegistryClient(ctrl)
	mockCluster := NewMockClusterClient(ctrl)
	mockFactory := NewMockClusterClientFactory(ctrl)

	service := &mcpserver.ReferenceService{
		Registry:       mockRegistry,
		ClusterFactory: mockFactory,
	}

	return service, mockRegistry, mockCluster, mockFactory
}

// NewTestCompareService creates a CompareService with gomock-generated mocks.
func NewTestCompareService(ctrl *gomock.Controller) (*mcpserver.CompareService, *MockHTTPDoer, *MockRegistryClient) {
	mockHTTP := NewMockHTTPDoer(ctrl)
	mockRegistry := NewMockRegistryClient(ctrl)

	service := &mcpserver.CompareService{
		HTTPClient: mockHTTP,
		Registry:   mockRegistry,
	}

	return service, mockHTTP, mockRegistry
}
