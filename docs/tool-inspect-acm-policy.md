# inspect_acm_policy

Extract structured violation details from an ACM Policy on the hub. Parses the deeply nested policy status and configuration templates to return per-cluster compliance, violation type, and affected resources.

## When to Use

- Quick status check: "Is this policy compliant?"
- Identify which spoke clusters are affected
- Get structured violation data from hub-side policy objects
- For deeper analysis with classified violations and suggested tool calls, use [`diagnose_acm_policy`](tool-diagnose-acm-policy.md) instead

## Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `policy_name` | string | Yes | Name of the ACM Policy (root or propagated). For propagated policies use the full dotted name. |
| `namespace` | string | No | Namespace of the Policy. If omitted, the tool searches all namespaces to find it. |
| `cluster` | string | No | Optional managed cluster name to filter violations |

## Output

```json
{
  "policy_name": "web-terminal-operator-status",
  "namespace": "ztp-policies",
  "compliance_state": "NonCompliant",
  "affected_clusters": [
    {"cluster_name": "sno-abi", "compliance_state": "NonCompliant"}
  ],
  "templates": [
    {"name": "web-terminal-operator-status-config", "kind": "ConfigurationPolicy"}
  ],
  "violations": [
    {
      "template_name": "web-terminal-operator-status-config",
      "cluster_name": "sno-abi",
      "violation_type": "missing",
      "resource_kind": "Operator",
      "resource_name": "web-terminal",
      "message": "operators [web-terminal] not found..."
    }
  ],
  "next_step": "Use openshift-mcp-server's resources_get with cluster='sno-abi' to inspect the violated resources."
}
```

## Violation Types

| Type | Meaning |
|------|---------|
| `missing` | Resource does not exist on the spoke |
| `drift` | Resource exists but differs from the policy template |
| `violation` | Generic compliance violation |
| `unknown` | Could not classify the violation |

## Follow-up

After calling this tool, use openshift-mcp-server tools with the `cluster` parameter:
- `resources_get` to inspect specific resources on managed clusters
- `trace_olm_subscription` for OLM-related violations (Subscription, CSV)

## Related Tools

- [`diagnose_acm_policy`](tool-diagnose-acm-policy.md) — deeper analysis with violation classification and suggested tool calls
- `resources_get` (openshift-mcp-server) — inspect resources on managed clusters
- `trace_olm_subscription` (openshift-mcp-server) — OLM chain tracing
