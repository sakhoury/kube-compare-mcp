# diagnose_acm_policy — primary tool for policy debugging

ALWAYS call `diagnose_acm_policy` when a user asks about:
- Any NonCompliant ACM policy
- Any policy in ztp-policies namespace
- Any propagated policy (format: namespace.policy-name)
- Debugging, diagnosing, or investigating policy compliance
- Why a policy is not compliant
- What is wrong with a policy

## Parameters

- `policy_name` (required): The policy name. Accepts root or propagated format.
- `namespace` (optional): Auto-discovered if omitted.
- `cluster` (optional): Focus on a specific managed cluster.

## Output

Returns JSON with per-cluster violations. Each violation has:
- `violation_type`: resource_missing, resource_drift, olm_stuck, crd_missing, unknown
- `resource_kind`, `resource_name`: the affected resource
- `desired_state`: what the policy expects (extracted from ConfigurationPolicy)
- `suggested_tool_call`: pre-filled parameters for the next investigation tool

## After receiving results

1. Execute EVERY `suggested_tool_call` automatically.
2. Compare actual spoke state with `desired_state`.
3. Report root cause and remediation.

Do NOT ask the user to run manual commands. Use the tools.
