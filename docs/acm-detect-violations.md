# acm_detect_violations

Scan an ACM hub cluster for policy violations across all managed clusters. Returns a summary of all non-compliant policies with severity, affected clusters, and remediation action.

## Overview

The `acm_detect_violations` tool connects to a Red Hat Advanced Cluster Management (ACM) hub cluster and reads all `Policy` resources from the `policy.open-cluster-management.io` API group. It reports which policies are compliant and which are not, along with severity levels, remediation actions, and the list of affected managed clusters.

This is typically the first ACM tool to use. After identifying violations, use `acm_diagnose_violation` with the same hub kubeconfig to deep-dive into a specific policy.

## Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `kubeconfig` | string | No | Kubeconfig for the ACM hub cluster. Accepts a registered target key (from `manage_targets`), base64-encoded kubeconfig, or raw kubeconfig YAML. When omitted, uses in-cluster or default config. |
| `context` | string | No | Kubernetes context name to use from the provided kubeconfig. |
| `namespace` | string | No | Filter policies by namespace. When omitted, scans all namespaces. |
| `severity` | string | No | Filter violations by minimum severity level: `low`, `medium`, `high`, or `critical`. |

## Response

The tool returns a JSON object with the following structure:

```json
{
  "total_policies": 5,
  "compliant": 3,
  "non_compliant": 2,
  "violations": [
    {
      "name": "require-resource-limits",
      "namespace": "open-cluster-management",
      "compliant": "NonCompliant",
      "severity": "high",
      "remediation_action": "inform",
      "categories": ["CM Configuration Management"],
      "affected_clusters": [
        { "name": "spoke1", "compliant": "NonCompliant" },
        { "name": "spoke2", "compliant": "Compliant" }
      ]
    }
  ]
}
```

### Response Fields

| Field | Description |
|-------|-------------|
| `total_policies` | Total number of policies found |
| `compliant` | Number of fully compliant policies |
| `non_compliant` | Number of policies with at least one violation |
| `violations` | Array of non-compliant policy summaries |
| `violations[].name` | Policy name |
| `violations[].namespace` | Policy namespace |
| `violations[].severity` | Severity level: `low`, `medium`, `high`, or `critical` |
| `violations[].remediation_action` | ACM remediation action: `inform` (report only) or `enforce` (auto-remediate) |
| `violations[].affected_clusters` | List of managed clusters and their compliance status for this policy |

## Prerequisites

- ACM must be installed on the target cluster (the `policy.open-cluster-management.io` CRD must exist)
- The kubeconfig must have read access to ACM Policy resources

## Example Prompts

```
Scan my cluster for ACM policy violations
```

```
Show me all critical ACM policy violations in the production namespace
```

```
List non-compliant policies on hub1/david with severity high or above
```

## Workflow

A typical ACM policy investigation workflow:

1. **Register the hub** using `manage_targets` with `action=add` and `target=hub-secret/namespace`
2. **Detect violations** using `acm_detect_violations` with `kubeconfig=hub-secret/namespace`
3. **Diagnose a specific violation** using `acm_diagnose_violation` with the policy name from step 2

## Error Handling

| Error | Cause | Resolution |
|-------|-------|------------|
| ACM Policy CRD not found | ACM is not installed on the target cluster | Install Red Hat Advanced Cluster Management |
| Failed to list policies | Insufficient RBAC permissions | Ensure the kubeconfig has `get`, `list`, `watch` on `policy.open-cluster-management.io` |
| No policies found | No policies exist in the specified namespace | Check the namespace or omit the namespace filter |
