# acm_diagnose_violation

Deep root-cause analysis of a specific ACM policy violation with automated remediation generation.

## Overview

The `acm_diagnose_violation` tool performs a comprehensive diagnosis of why an ACM (Advanced Cluster Management) policy is non-compliant. It connects to the ACM hub cluster, reads the policy definition and its object templates, then automatically connects to the affected managed cluster to inspect the actual resources.

The tool runs a multi-step root cause analysis (RCA) decision tree and generates remediation YAML with step-by-step instructions for each violation.

## Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `kubeconfig` | string | No | Kubeconfig for the ACM hub cluster. Point this at the hub and the tool automatically extracts the managed cluster kubeconfig from hub secrets. Accepts a registered target key (from `manage_targets`), base64-encoded kubeconfig, or raw kubeconfig YAML. When omitted, uses in-cluster config. |
| `context` | string | No | Kubernetes context name to use from the provided kubeconfig. |
| `policy_name` | string | Yes | Name of the ACM policy to diagnose. |
| `policy_namespace` | string | No | Namespace of the ACM policy. If omitted, searches all namespaces. |
| `managed_cluster` | string | No | Name of the managed/spoke cluster to inspect. The tool connects to this cluster automatically through the hub. When omitted, auto-detects the cluster from the policy status. |
| `verbose` | boolean | No | Include a `diagnostic_trace` for each violation showing which RCA steps ran and the decision at each step. Defaults to `false`. |

## Root Cause Analysis

The diagnosis tool performs a multi-step root cause analysis (RCA) decision tree for each object template in the policy. Steps are executed in order and the tree **short-circuits** as soon as a definitive root cause is found -- remaining steps are skipped (and marked as such in the diagnostic trace when `verbose=true`).

The analysis takes two different paths depending on whether the target resource exists on the managed cluster.

---

### Step 0: Resource Lookup

Fetches the target resource from the managed cluster using the kind, name, and namespace extracted from the policy's `objectDefinition`. This determines which analysis path to follow:

- **Resource exists** -- proceeds to the "resource exists" path (Steps 1-5)
- **Resource missing** -- proceeds to the "resource missing" path (Steps A-D)

Additionally, when the resource exists, the tool computes a **field-level diff** between the policy's desired state (`objectDefinition`) and the actual resource on the cluster. Differing fields are reported in the `differing_fields` array of the response.

---

### Resource Exists Path

When the resource is found on the cluster, the tool investigates why it doesn't match the policy's desired state.

#### OLM Subscription Analysis (conditional)

Runs **before** the main steps, but **only** when the target resource is an OLM `Subscription` (`operators.coreos.com/v1alpha1`). This step performs domain-specific checks:

- **Pending manual approval** -- Detects when `spec.installPlanApproval` is `Manual` and `status.state` is `UpgradePending`. This means a new operator version is available but the InstallPlan has not been approved. The tool also compares `status.currentCSV` vs `status.installedCSV` to identify the pending upgrade.
- **Unhealthy catalog source** -- Reads `status.catalogHealth` to check whether the `CatalogSource` backing the subscription is healthy. An unhealthy catalog prevents the subscription from resolving available updates.

If an OLM-specific issue is found, it becomes the root cause immediately (e.g., `subscription_pending_approval` or `missing_dependency`) and the remaining steps are skipped.

#### Step 1: Ownership Analysis

Determines whether the resource is managed by an external controller that would revert direct patches. The analysis checks three sources:

1. **`metadata.managedFields`** -- Parses the server-side apply field ownership entries. Each entry's `manager` name is compared against known external tool keywords: `argocd`, `argo-cd`, `flux`, `kustomize-controller`, `helm-controller`, `helm`, `tiller`, `terraform`, `pulumi`, `crossplane`, `ansible`, `ansible-operator`. When an external manager is found, the specific field paths it owns are extracted from the `FieldsV1` structure.

2. **Ownership annotations and labels** -- Checks for well-known annotations that indicate GitOps or declarative management:
   - `argocd.argoproj.io/managed-by` (ArgoCD)
   - `meta.helm.sh/release-name` (Helm)
   - `kustomize.toolkit.fluxcd.io/name` (Flux Kustomization)
   - `helm.toolkit.fluxcd.io/name` (Flux HelmRelease)
   - `flux.weave.works/sync-checksum` (Flux v1)
   - `crossplane.io/composition-resource-name` (Crossplane)
   - `app.kubernetes.io/managed-by` label (any external manager)

3. **`metadata.ownerReferences`** -- Checks for controller owner references (where `controller: true`), which indicate a parent resource actively reconciling this object.

If an external owner is found, the root cause is `active_reconciliation` and the remediation directs the user to update the source system rather than patching the resource directly. The `fix_target` field provides specific guidance (e.g., "Update the ArgoCD Application source (Git repository) with the required changes" or "Update the Helm chart values or templates and run helm upgrade").

#### Step 2: Dependency Validation

Recursively walks the policy's `objectDefinition` JSON tree and collects all resource references, then verifies each referenced resource exists on the cluster. The following reference patterns are recognized:

**Direct field references:**
- `secretName` -> Secret
- `configMapName` -> ConfigMap
- `serviceAccountName` -> ServiceAccount
- `storageClassName` -> StorageClass
- `claimName` -> PersistentVolumeClaim
- `ingressClassName` -> IngressClass

**Nested object references** (objects with a `name` sub-field):
- `secretRef` / `secretKeyRef` -> Secret
- `configMapRef` / `configMapKeyRef` -> ConfigMap

**Volume source references:**
- `volumes[].configMap.name` -> ConfigMap
- `volumes[].secret.secretName` -> Secret
- `volumes[].persistentVolumeClaim.claimName` -> PersistentVolumeClaim

**CRD availability** -- If the resource's API group is not a core Kubernetes group, the tool also verifies that the corresponding CustomResourceDefinition is installed on the cluster.

If any dependency is missing, the root cause is `missing_dependency` with details on which resource does not exist.

#### Step 3: Mutability Check

Submits a **server-side dry-run patch** (`DryRun=All`) to the Kubernetes API server using the policy's `objectDefinition` as the patch payload. This tests whether the remediation patch would be accepted without actually modifying any resources. The API server response is analyzed to classify rejections:

| Rejection | Root Cause | Description |
|-----------|------------|-------------|
| "field is immutable" / "immutable" | `immutable_field` | The patch touches a field that cannot be changed after creation (e.g., `spec.selector` on a Deployment). The resource must be deleted and recreated. The specific immutable field name is extracted from the error when possible. |
| "denied the request" / "admission webhook" | `admission_blocked` | An admission webhook rejected the change. The webhook name is extracted from the error message. |
| "forbidden" / "cannot patch" | `rbac_denied` | The service account lacks RBAC permissions to patch this resource type. |
| "exceeded quota" / "resourcequota" | `quota_exceeded` | A ResourceQuota prevents the change (e.g., increasing resource requests beyond the namespace quota). |

If the dry-run patch succeeds, the step passes and the analysis continues.

#### Step 4: Conflict Detection

Searches for admission controllers and policies that might block or interfere with remediation. Four types of conflicts are checked:

1. **ValidatingWebhookConfigurations** -- Lists all validating webhooks and checks whether their `rules` match the target resource's API group and resource type. Matching webhooks may reject changes. The `failurePolicy` (Fail or Ignore) is reported to distinguish blocking vs non-blocking webhooks.

2. **MutatingWebhookConfigurations** -- Same as above but for mutating webhooks. These may silently alter the patch, causing the resource to diverge from the policy's desired state even after remediation.

3. **Gatekeeper constraints** -- Lists all `constraints.gatekeeper.sh` resources and checks whether their `spec.match.kinds` include the target resource kind. The `spec.enforcementAction` (deny, dryrun, warn) determines whether the constraint would block changes. Existing violation messages from `status.violations` are included.

4. **Other ACM policies** -- Lists all non-compliant ACM policies and checks whether any reference the same resource (by kind and name in the policy status message). These indicate competing policy requirements that may conflict.

Only conflicts with blocking enforcement (`deny`, `enforce`, `fail`) trigger a `policy_conflict` root cause. Audit-only conflicts are reported but do not short-circuit.

#### Step 5: Event History

Fetches Kubernetes events related to the target resource (by name, namespace, and kind). Events are sorted with **Warning events first**, then by most recent timestamp. The tool returns up to 20 events and examines Warning events for clues about recurring failures. Up to 5 warning events are included as evidence.

If warning events are found but no earlier step identified a definitive root cause, the tool returns `unknown` with low confidence, including the event details as hints for manual investigation.

If all 5 steps pass with no blocking issues found, the root cause is `direct_fix_applicable` with high confidence, meaning the resource can be patched directly to achieve compliance.

---

### Resource Missing Path

When the resource does not exist on the managed cluster, the tool investigates **why** it is missing rather than simply reporting `resource_not_found`. Four sub-steps are executed in order:

#### Step A: Namespace Health

Checks whether the target namespace exists and is in a healthy state:

- **Namespace does not exist** -- Root cause is `namespace_missing`. The resource cannot exist without its namespace. This often indicates the namespace or its owning operator was never installed.
- **Namespace is Terminating** -- Root cause is `namespace_terminating`. The namespace is being deleted, so new resources cannot be created in it. This typically indicates the namespace or its operator was uninstalled.
- **Namespace is Active** -- The namespace is healthy; the analysis continues to the next sub-step.
- **Cluster-scoped resource** -- This step is skipped when the resource has no namespace.

#### Step B: CRD Registration

Checks whether the Custom Resource Definition for the target resource is installed on the cluster. This step is skipped for core Kubernetes API types (e.g., Pods, Services, ConfigMaps).

For custom resources (any non-core API group), the tool constructs the expected CRD name (e.g., `subscriptions.operators.coreos.com`) and verifies it exists via the `apiextensions.k8s.io` API. If the CRD is missing, the root cause is `api_not_registered` -- the operator that provides this resource type needs to be installed first.

#### Step C: RBAC Visibility

Performs a `SelfSubjectAccessReview` to verify the current service account has `GET` permission for the target resource type in the target namespace. If the resource type exists but the service account cannot see it, the root cause is `rbac_not_visible` with medium confidence -- the resource may actually exist but is invisible due to RBAC restrictions.

#### Step D: Event History (for missing resources)

Same as Step 5 in the resource-exists path, but in the context of a missing resource. Warning events (e.g., failed creation attempts, eviction events) may explain why the resource was deleted or never created. If warning events are found, they are included as evidence alongside the `resource_not_found` cause.

If all sub-steps pass (namespace is healthy, CRD is registered, RBAC allows visibility, no warning events), the root cause is `resource_not_found` with high confidence. This means the resource was likely never created or was intentionally deleted.

---

### Hub Diagnostics

When the kubeconfig points to an ACM hub cluster, the tool performs additional steps before the RCA:

1. **Hub detection** -- Checks for the presence of `MultiClusterHub` CRD to confirm this is an ACM hub.
2. **Managed cluster resolution** -- If `managed_cluster` is specified, uses that cluster. Otherwise, extracts the non-compliant cluster name from the policy's compliance status.
3. **Kubeconfig extraction** -- Retrieves the managed cluster's admin kubeconfig from hub secrets. It first tries the `ClusterDeployment` secret reference (Hive), then falls back to well-known secret naming conventions.
4. **Remote inspector** -- Uses the extracted kubeconfig to connect to the managed cluster's API server, so all RCA steps run against the actual spoke cluster, not the hub.

The response includes a `hub_diagnostic` section reporting which cluster was inspected and the kubeconfig source used.

---

### Cascading Failure Detection

After all templates are analyzed, the tool performs a post-processing pass to identify **cascading failures** -- violations that are consequences of other violations rather than independent root causes:

- **OLM cascading** -- If an OLM `Subscription` is non-compliant in a namespace, any `Operator` resource in the same namespace that is also non-compliant is marked as cascading from the Subscription issue.
- **Namespace cascading** -- If a `Namespace` is missing or terminating, all resources in that namespace are marked as cascading from the namespace issue.

Cascading violations include a `cascading_from` field in the response, helping distinguish primary issues from their downstream effects.

## Root Cause Categories

| Cause | Description |
|-------|-------------|
| `resource_not_found` | The target resource does not exist on the cluster |
| `direct_fix_applicable` | A field diff exists and can be patched directly |
| `active_reconciliation` | An external controller (ArgoCD, Helm, Flux) manages this resource |
| `missing_dependency` | A referenced resource (Secret, ConfigMap, etc.) does not exist |
| `immutable_field` | The patch touches an immutable field |
| `admission_blocked` | A webhook or admission controller rejects the change |
| `rbac_denied` | Insufficient RBAC permissions to patch the resource |
| `quota_exceeded` | Resource quota prevents the change |
| `policy_conflict` | Another policy or admission controller conflicts |
| `subscription_pending_approval` | An OLM Subscription requires manual install plan approval |
| `namespace_terminating` | The target namespace is being deleted |
| `namespace_missing` | The target namespace does not exist |
| `api_not_registered` | The CRD for the target resource is not installed |
| `rbac_not_visible` | The service account cannot see this resource type |
| `unknown` | No specific root cause identified |

## Response

```json
{
  "policy": {
    "name": "require-resource-limits",
    "namespace": "open-cluster-management",
    "compliant": "NonCompliant",
    "severity": "high",
    "templates": [...]
  },
  "needs_action_count": 1,
  "compliant_count": 0,
  "violations": [
    {
      "template": {
        "name": "require-limits-config",
        "kind": "LimitRange",
        "api_version": "v1",
        "resource_name": "resource-limits",
        "resource_namespace": "production"
      },
      "resource": {
        "kind": "LimitRange",
        "name": "resource-limits",
        "namespace": "production",
        "exists": false
      },
      "status": "needs_action",
      "root_cause": {
        "primary_cause": "resource_not_found",
        "confidence": "high",
        "detail": "The LimitRange/resource-limits resource does not exist in namespace production"
      },
      "remediation": {
        "direct_patch_works": true,
        "patch_yaml": "apiVersion: v1\nkind: LimitRange\n...",
        "patch_source": "policy_object_definition",
        "confidence": "high",
        "steps": [
          "Save the remediation YAML to a file (e.g., remediation.yaml)",
          "Apply: kubectl apply -f remediation.yaml -n production",
          "Verify the ACM policy becomes compliant after the change"
        ]
      },
      "diagnostic_trace": {
        "steps": [
          {
            "name": "resource_lookup",
            "status": "executed",
            "duration": "1ms",
            "finding": "Resource not found",
            "decision": "root_cause_found"
          }
        ]
      }
    }
  ],
  "hub_diagnostic": {
    "is_hub": true,
    "target_cluster": "spoke1",
    "kubeconfig_source": "ClusterDeployment spoke1/spoke1-admin-kubeconfig"
  },
  "summary": "Policy 'open-cluster-management/require-resource-limits' is NonCompliant..."
}
```

## Prerequisites

- ACM must be installed on the hub cluster
- The kubeconfig must point to the ACM hub cluster
- The hub must have `ClusterDeployment` resources (Hive) or well-known kubeconfig secrets for managed clusters
- RBAC permissions for reading policies, secrets, and performing dry-run patches (see [RBAC Requirements](#rbac-requirements))

## RBAC Requirements

| Permission | Purpose |
|-----------|---------|
| `get`, `list`, `watch` on `policy.open-cluster-management.io` policies | Read ACM policies and compliance status |
| `get`, `list` on secrets | Read managed cluster kubeconfigs from hub |
| `get`, `list` on `hive.openshift.io` clusterdeployments | Find managed cluster kubeconfig secret references |
| `get`, `list` on `cluster.open-cluster-management.io` managedclusters | List managed clusters |
| `get`, `list` on `admissionregistration.k8s.io` webhooks | Conflict detection with admission webhooks |
| `get`, `list` on `constraints.gatekeeper.sh` resources | Conflict detection with Gatekeeper constraints |
| `create` on `authorization.k8s.io` selfsubjectaccessreviews | RBAC permission checking during RCA |
| `patch` on `*/*` (dry-run only) | Mutability checking via server-side dry-run patches |

> **Note:** The `patch` permission is used exclusively with `DryRun=All` -- no actual modifications are made to cluster resources.

## Example Prompts

```
Diagnose the ACM policy violation for require-resource-limits
```

```
Why is the require-labels policy non-compliant? Provide root cause and remediation steps.
```

```
Diagnose policy install-operator on managed cluster spoke1 using hub hub1/david
```

## Workflow

1. **Register the hub** using `manage_targets` with `action=add`
2. **Detect violations** using `acm_detect_violations` to find non-compliant policies
3. **Diagnose** using `acm_diagnose_violation` with the policy name from step 2
4. **Apply remediation** using the generated YAML and step-by-step instructions
5. **Verify** the policy becomes compliant after applying the fix
