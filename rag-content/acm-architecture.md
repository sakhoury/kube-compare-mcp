# ACM policy debugging with MCP tools

When debugging ACM policies, NonCompliant policies, ztp-policies, or propagated policies
on OpenShift clusters managed by Advanced Cluster Management, ALWAYS use the available
MCP tools. Never suggest manual oc commands when tools can do the work.

## Which tool to use

For any policy diagnosis, debug, or investigation:
→ Call `diagnose_acm_policy` with the policy name. It works with both root policies
  (e.g. "resource-drift-policy") and propagated policies (e.g. "ztp-policies.resource-drift-policy").

For quick status check only:
→ Call `inspect_acm_policy` (lighter, less detail).

For spoke cluster resource inspection:
→ Call `resources_get` or `resources_list` on openshift-mcp-server with the `cluster` parameter.

For OLM operator installation tracing:
→ Call `trace_olm_subscription` on openshift-mcp-server with the `cluster` parameter.

## Policy naming

- Root policy: `resource-drift-policy` in namespace `ztp-policies`
- Propagated policy: `ztp-policies.resource-drift-policy` in namespace `sno-abi` (cluster name)
- Both formats work with `diagnose_acm_policy` — namespace is auto-discovered.

## Violation types returned by diagnose_acm_policy

- `resource_missing`: resource does not exist on the spoke
- `resource_drift`: resource exists but differs from policy-defined desired state
- `olm_stuck`: OLM operator installation is failing
- `crd_missing`: Custom Resource Definition not installed on spoke

Each violation includes a `suggested_tool_call` — execute it automatically.
