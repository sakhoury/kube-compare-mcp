# diagnose_acm_policy

Diagnoses a NonCompliant ACM policy by analysing hub-side policy data. Classifies each violation and returns structured JSON with suggested next tool calls.

## Parameters

| Parameter | Required | Description |
|-----------|----------|-------------|
| `policy_name` | Yes | Name of the ACM Policy (root or propagated) |
| `namespace` | No | Policy namespace. Auto-discovered if omitted. |
| `cluster` | No | Filter diagnosis to a specific managed cluster |

## Output

Returns JSON with:

- `compliance_state`: Overall policy compliance
- `clusters[]`: Per-cluster diagnosis containing:
  - `issues[]`: Each with:
    - `violation_type`: `resource_missing`, `resource_drift`, `olm_stuck`, `crd_missing`, or `unknown`
    - `resource_kind`, `resource_name`, `resource_namespace`
    - `desired_state`: What the policy expects (from ConfigurationPolicy object-templates)
    - `suggested_tool_call`: Pre-filled parameters for the next investigation step

## Violation Types

| Type | Meaning | Suggested Follow-up |
|------|---------|-------------------|
| `resource_missing` | Resource does not exist on the managed cluster | `resources_get` on openshift-mcp-server to confirm |
| `resource_drift` | Resource exists but doesn't match desired state | `resources_get` to compare actual vs desired |
| `olm_stuck` | OLM-related resource (Subscription, CSV) is failing | `trace_olm_subscription` on openshift-mcp-server |
| `crd_missing` | Custom Resource Definition not installed | `resources_get` for the CRD on the managed cluster |
| `unknown` | Could not classify the violation | Manual inspection via `resources_get` |

## Related Tools

- [`inspect_acm_policy`](tool-inspect-acm-policy.md) — lighter-weight policy status check
- `trace_olm_subscription` (openshift-mcp-server) — OLM chain tracing on managed clusters
- `resources_get` (openshift-mcp-server) — resource inspection on managed clusters
