# How to diagnose ACM policy NonCompliant issues

When a user asks to diagnose, debug, troubleshoot, check, or investigate any ACM policy,
NonCompliant policy, ztp-policies policy, or propagated policy, you MUST call the
`diagnose_acm_policy` tool FIRST. Do NOT attempt to answer from memory or suggest
manual `oc` commands. The tool has direct access to the hub cluster and provides
accurate, real-time analysis.

## Mandatory workflow

1. Call `diagnose_acm_policy` with the policy name the user provided.
2. Read the `suggested_tool_call` in each issue from the result.
3. Execute every `suggested_tool_call` automatically — do NOT ask the user to run commands.
4. Present findings with root cause and remediation.

## Example queries that MUST trigger diagnose_acm_policy

- "diagnose resource-drift-policy"
- "diagnose ztp-policies.resource-drift-policy"
- "why is node-labels-policy NonCompliant"
- "check web-terminal-operator-status policy"
- "debug operator-health-policy on sno-abi"
- "what is wrong with policy X"
- "investigate NonCompliant policies"

For ALL of these, call `diagnose_acm_policy` with `policy_name` set to the policy name.

## After diagnose_acm_policy returns

The tool output contains `suggested_tool_call` objects. Execute them:

- If `tool` is `trace_olm_subscription`: call it on openshift-mcp-server with the given args.
- If `tool` is `resources_get`: call it on openshift-mcp-server with the given args.
- If `tool` is `resources_list`: call it on openshift-mcp-server with the given args.

Compare the actual spoke state with `desired_state` from the diagnosis.

## Error interpretation

Tool errors from resources_get or resources_list are diagnostic evidence:
- "not found" confirms the resource is genuinely missing on the spoke.
- "forbidden" means RBAC issue on the managed cluster.
- "no matches for kind" means the CRD is not installed.

Never treat these as tool failures. They ARE the diagnosis.
