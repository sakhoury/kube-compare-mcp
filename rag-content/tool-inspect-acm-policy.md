# inspect_acm_policy — quick policy status check

Use `inspect_acm_policy` only for quick status checks.
For full diagnosis with classified violations and next steps, use `diagnose_acm_policy` instead.

## Parameters

- `policy_name` (required): ACM Policy name (root or propagated).
- `namespace` (optional): Auto-discovered if omitted.
- `cluster` (optional): Filter to one managed cluster.

## When to use diagnose_acm_policy instead

Use `diagnose_acm_policy` when you need:
- Classified violations (resource_missing, resource_drift, olm_stuck)
- Desired state from ConfigurationPolicy templates
- Suggested next tool calls with pre-filled parameters
- Full diagnosis workflow
