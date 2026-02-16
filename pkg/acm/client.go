// SPDX-License-Identifier: Apache-2.0

package acm

import (
	"context"
	"fmt"
	"strings"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// ACM Policy GVR constants.
var (
	policyGVR = schema.GroupVersionResource{
		Group:    "policy.open-cluster-management.io",
		Version:  "v1",
		Resource: "policies",
	}
)

// DefaultACMClientFactory is the production implementation of ACMClientFactory.
type DefaultACMClientFactory struct{}

// NewPolicyLister creates a DefaultPolicyLister from the given rest.Config.
func (f *DefaultACMClientFactory) NewPolicyLister(config *rest.Config) (PolicyLister, error) {
	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}
	return &DefaultPolicyLister{client: dynClient}, nil
}

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

// Package-level defaults.
var DefaultACMFactory ACMClientFactory = &DefaultACMClientFactory{}

// --- DefaultPolicyLister ---

// DefaultPolicyLister is the production implementation of PolicyLister.
type DefaultPolicyLister struct {
	client dynamic.Interface
}

// ListPolicies lists all ACM Policy CRs, optionally filtered by namespace.
func (l *DefaultPolicyLister) ListPolicies(ctx context.Context, namespace string) ([]PolicySummary, error) {
	var policyList *unstructured.UnstructuredList
	var err error

	if namespace != "" {
		policyList, err = l.client.Resource(policyGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	} else {
		policyList, err = l.client.Resource(policyGVR).List(ctx, metav1.ListOptions{})
	}

	if err != nil {
		if isNotFoundOrCRDMissing(err) {
			return nil, fmt.Errorf("%w: the ACM Policy CRD (policy.open-cluster-management.io) was not found, "+
				"ensure Red Hat Advanced Cluster Management is installed on this cluster", ErrACMNotInstalled)
		}
		return nil, fmt.Errorf("failed to list ACM policies: %w. "+
			"Verify the cluster is accessible and you have permission to read Policy resources", err)
	}

	var summaries []PolicySummary
	for i := range policyList.Items {
		summaries = append(summaries, parsePolicySummary(&policyList.Items[i]))
	}

	return summaries, nil
}

// GetPolicy returns full detail for a specific ACM policy.
func (l *DefaultPolicyLister) GetPolicy(ctx context.Context, name, namespace string) (*PolicyDetail, error) {
	var policy *unstructured.Unstructured
	var err error

	if namespace != "" {
		policy, err = l.client.Resource(policyGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	} else {
		// Search across all namespaces
		list, listErr := l.client.Resource(policyGVR).List(ctx, metav1.ListOptions{})
		if listErr != nil {
			if isNotFoundOrCRDMissing(listErr) {
				return nil, fmt.Errorf("%w: the ACM Policy CRD was not found on this cluster", ErrACMNotInstalled)
			}
			return nil, fmt.Errorf("failed to search for policy: %w. "+
				"Verify cluster access permissions", listErr)
		}
		for i := range list.Items {
			if list.Items[i].GetName() == name {
				policy = &list.Items[i]
				break
			}
		}
		if policy == nil {
			return nil, fmt.Errorf("%w: Policy '%s' was not found in any namespace", ErrPolicyNotFound, name)
		}
		err = nil
	}

	if err != nil {
		if isNotFoundOrCRDMissing(err) {
			return nil, fmt.Errorf("%w: Policy '%s' was not found in namespace '%s'", ErrPolicyNotFound, name, namespace)
		}
		return nil, fmt.Errorf("failed to get policy: %w. "+
			"Verify the policy name and namespace are correct", err)
	}

	detail := parsePolicyDetail(policy)
	enrichTemplatesWithComplianceMessages(policy, detail)
	return detail, nil
}

// --- DefaultResourceInspector ---

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

// ListValidatingWebhooks returns all ValidatingWebhookConfigurations.
func (i *DefaultResourceInspector) ListValidatingWebhooks(ctx context.Context) ([]WebhookInfo, error) {
	list, err := i.kubeClient.AdmissionregistrationV1().ValidatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list validating webhooks: %w", err)
	}

	var webhooks []WebhookInfo
	for _, vwc := range list.Items {
		for _, wh := range vwc.Webhooks {
			webhooks = append(webhooks, convertWebhook(vwc.Name, "validating", wh.Name,
				wh.FailurePolicy, wh.ClientConfig, wh.Rules))
		}
	}
	return webhooks, nil
}

// ListMutatingWebhooks returns all MutatingWebhookConfigurations.
func (i *DefaultResourceInspector) ListMutatingWebhooks(ctx context.Context) ([]WebhookInfo, error) {
	list, err := i.kubeClient.AdmissionregistrationV1().MutatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list mutating webhooks: %w", err)
	}

	var webhooks []WebhookInfo
	for _, mwc := range list.Items {
		for _, wh := range mwc.Webhooks {
			webhooks = append(webhooks, convertWebhook(mwc.Name, "mutating", wh.Name,
				wh.FailurePolicy, wh.ClientConfig, wh.Rules))
		}
	}
	return webhooks, nil
}

// ListEvents returns events for a specific resource.
func (i *DefaultResourceInspector) ListEvents(ctx context.Context, name, namespace, kind string) ([]EventInfo, error) {
	fieldSelector := fmt.Sprintf("involvedObject.name=%s,involvedObject.kind=%s", name, kind)

	var events []EventInfo
	ns := namespace
	if ns == "" {
		ns = "default"
	}

	list, err := i.kubeClient.CoreV1().Events(ns).List(ctx, metav1.ListOptions{
		FieldSelector: fieldSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("list events for %s/%s in %q: %w", kind, name, ns, err)
	}

	for _, e := range list.Items {
		events = append(events, EventInfo{
			Reason:    e.Reason,
			Message:   e.Message,
			Source:    e.Source.Component,
			Count:     e.Count,
			Type:      e.Type,
			FirstSeen: e.FirstTimestamp.Format(time.RFC3339),
			LastSeen:  e.LastTimestamp.Format(time.RFC3339),
		})
	}

	return events, nil
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

// --- Internal helpers ---

func convertWebhook(configName, whType, whName string,
	failPolicy *admissionregistrationv1.FailurePolicyType,
	clientConfig admissionregistrationv1.WebhookClientConfig,
	rules []admissionregistrationv1.RuleWithOperations) WebhookInfo {

	fp := "Fail"
	if failPolicy != nil {
		fp = string(*failPolicy)
	}

	var service string
	if clientConfig.Service != nil {
		service = fmt.Sprintf("%s/%s", clientConfig.Service.Namespace, clientConfig.Service.Name)
	}

	var webhookRules []WebhookRule
	for _, rule := range rules {
		var ops []string
		for _, op := range rule.Operations {
			ops = append(ops, string(op))
		}
		webhookRules = append(webhookRules, WebhookRule{
			APIGroups:   rule.APIGroups,
			APIVersions: rule.APIVersions,
			Resources:   rule.Resources,
			Operations:  ops,
		})
	}

	return WebhookInfo{
		ConfigName:  configName,
		WebhookName: whName,
		Type:        whType,
		Service:     service,
		FailPolicy:  fp,
		Rules:       webhookRules,
	}
}

// parsePolicySummary extracts a PolicySummary from an unstructured ACM Policy.
func parsePolicySummary(obj *unstructured.Unstructured) PolicySummary {
	summary := PolicySummary{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}

	// status.compliant
	compliant, _, _ := unstructured.NestedString(obj.Object, "status", "compliant")
	summary.Compliant = compliant

	// spec.remediationAction
	remAction, _, _ := unstructured.NestedString(obj.Object, "spec", "remediationAction")
	summary.RemediationAction = remAction

	// Annotations for severity and categories
	annotations := obj.GetAnnotations()
	if sev, ok := annotations["policy.open-cluster-management.io/severity"]; ok {
		summary.Severity = sev
	}
	if cats, ok := annotations["policy.open-cluster-management.io/categories"]; ok {
		summary.Categories = strings.Split(cats, ",")
		for i := range summary.Categories {
			summary.Categories[i] = strings.TrimSpace(summary.Categories[i])
		}
	}

	// Severity may also come from the embedded ConfigurationPolicy
	if summary.Severity == "" {
		summary.Severity = extractSeverityFromTemplates(obj)
	}

	// status.status for per-cluster compliance
	statusList, _, _ := unstructured.NestedSlice(obj.Object, "status", "status")
	for _, item := range statusList {
		if clusterMap, ok := item.(map[string]interface{}); ok {
			cs := ClusterStatus{}
			cs.Name, _, _ = unstructured.NestedString(clusterMap, "clustername")
			cs.Compliant, _, _ = unstructured.NestedString(clusterMap, "compliant")
			if cs.Name != "" {
				summary.AffectedClusters = append(summary.AffectedClusters, cs)
			}
		}
	}

	return summary
}

// extractSeverityFromTemplates reads severity from the first ConfigurationPolicy template.
func extractSeverityFromTemplates(obj *unstructured.Unstructured) string {
	templates, _, _ := unstructured.NestedSlice(obj.Object, "spec", "policy-templates")
	for _, tmpl := range templates {
		tmplMap, ok := tmpl.(map[string]interface{})
		if !ok {
			continue
		}
		sev, _, _ := unstructured.NestedString(tmplMap, "objectDefinition", "spec", "severity")
		if sev != "" {
			return sev
		}
	}
	return ""
}

// parsePolicyDetail extracts full PolicyDetail including ConfigurationPolicy templates.
func parsePolicyDetail(obj *unstructured.Unstructured) *PolicyDetail {
	detail := &PolicyDetail{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}

	compliant, _, _ := unstructured.NestedString(obj.Object, "status", "compliant")
	detail.Compliant = compliant

	remAction, _, _ := unstructured.NestedString(obj.Object, "spec", "remediationAction")
	detail.RemediationAction = remAction

	annotations := obj.GetAnnotations()
	if sev, ok := annotations["policy.open-cluster-management.io/severity"]; ok {
		detail.Severity = sev
	}

	// Parse per-cluster compliance from status.status
	statusList, _, _ := unstructured.NestedSlice(obj.Object, "status", "status")
	for _, item := range statusList {
		if clusterMap, ok := item.(map[string]interface{}); ok {
			cs := ClusterStatus{}
			cs.Name, _, _ = unstructured.NestedString(clusterMap, "clustername")
			cs.Compliant, _, _ = unstructured.NestedString(clusterMap, "compliant")
			if cs.Name != "" {
				detail.AffectedClusters = append(detail.AffectedClusters, cs)
			}
		}
	}

	// Parse policy-templates to extract ConfigurationPolicy object-templates
	templates, _, _ := unstructured.NestedSlice(obj.Object, "spec", "policy-templates")
	for _, tmpl := range templates {
		tmplMap, ok := tmpl.(map[string]interface{})
		if !ok {
			continue
		}

		objDef, _, _ := unstructured.NestedMap(tmplMap, "objectDefinition")
		if objDef == nil {
			continue
		}

		configPolicyName, _, _ := unstructured.NestedString(objDef, "metadata", "name")
		sev, _, _ := unstructured.NestedString(objDef, "spec", "severity")
		if detail.Severity == "" && sev != "" {
			detail.Severity = sev
		}

		objectTemplates, _, _ := unstructured.NestedSlice(objDef, "spec", "object-templates")
		for _, ot := range objectTemplates {
			otMap, ok := ot.(map[string]interface{})
			if !ok {
				continue
			}

			complianceType, _, _ := unstructured.NestedString(otMap, "complianceType")
			innerObjDef, _, _ := unstructured.NestedMap(otMap, "objectDefinition")
			if innerObjDef == nil {
				continue
			}

			apiVersion, _ := innerObjDef["apiVersion"].(string)
			kind, _ := innerObjDef["kind"].(string)

			var resName, resNamespace string
			if metadata, ok := innerObjDef["metadata"].(map[string]interface{}); ok {
				resName, _ = metadata["name"].(string)
				resNamespace, _ = metadata["namespace"].(string)
			}

			detail.Templates = append(detail.Templates, ConfigPolicyTemplate{
				Name:              configPolicyName,
				ComplianceType:    complianceType,
				ObjectDefinition:  innerObjDef,
				Kind:              kind,
				APIVersion:        apiVersion,
				ResourceName:      resName,
				ResourceNamespace: resNamespace,
			})
		}
	}

	return detail
}

// enrichTemplatesWithComplianceMessages parses status.details from the ACM policy
// and attaches the most recent compliance message to each matching ConfigPolicyTemplate.
func enrichTemplatesWithComplianceMessages(policy *unstructured.Unstructured, detail *PolicyDetail) {
	details, _, _ := unstructured.NestedSlice(policy.Object, "status", "details")
	if len(details) == 0 {
		return
	}

	// Build a map of templateMeta.name -> most recent message
	messagesByTemplate := make(map[string]string)
	for _, d := range details {
		dMap, ok := d.(map[string]interface{})
		if !ok {
			continue
		}
		tmplName, _, _ := unstructured.NestedString(dMap, "templateMeta", "name")
		if tmplName == "" {
			continue
		}

		history, _, _ := unstructured.NestedSlice(dMap, "history")
		if len(history) == 0 {
			continue
		}
		// Most recent event is first
		if histEntry, ok := history[0].(map[string]interface{}); ok {
			if msg, ok := histEntry["message"].(string); ok {
				messagesByTemplate[tmplName] = msg
			}
		}
	}

	// Attach messages to templates.
	for i := range detail.Templates {
		tmpl := &detail.Templates[i]

		fullMsg, ok := messagesByTemplate[tmpl.Name]
		if !ok {
			continue
		}

		resourceLower := strings.ToLower(tmpl.ResourceName)
		kindPlural := strings.ToLower(KindToResource(tmpl.Kind))

		parts := strings.Split(fullMsg, ";")
		for _, part := range parts {
			partLower := strings.ToLower(strings.TrimSpace(part))
			if resourceLower != "" && strings.Contains(partLower, resourceLower) {
				tmpl.ComplianceMessage = strings.TrimSpace(part)
				break
			}
			if kindPlural != "" && strings.Contains(partLower, kindPlural) {
				tmpl.ComplianceMessage = strings.TrimSpace(part)
			}
		}

		// Fallback: if no specific match, use the full message
		if tmpl.ComplianceMessage == "" {
			tmpl.ComplianceMessage = fullMsg
		}
	}
}

// isNotFoundOrCRDMissing checks if an error indicates the resource or CRD doesn't exist.
func isNotFoundOrCRDMissing(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "the server could not find the requested resource") ||
		strings.Contains(errStr, "not found") ||
		strings.Contains(errStr, "no matches for kind")
}
