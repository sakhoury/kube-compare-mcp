# manage_targets

Manage target cluster kubeconfigs for use by all tools. Register, list, remove, and discover cluster kubeconfigs that can be referenced by any tool's `kubeconfig` parameter.

## Overview

The `manage_targets` tool provides a unified way to manage cluster access across all MCP tools. Instead of passing raw kubeconfig content to every tool call, you can register clusters once and reference them by a short key (e.g., `hub1/david` or `spoke1`).

Targets are stored in-memory and support two backing modes:

- **Secret-backed** -- Kubeconfig stored as a Kubernetes Secret on the MCP server's local cluster. Registered via the `add` action.
- **Discovered** -- Kubeconfig extracted from an ACM hub's ClusterDeployment secrets. Registered via the `discover` action.

Any registered target key can be passed as the `kubeconfig` parameter to any tool (`kube_compare_cluster_diff`, `kube_compare_validate_rds`, `baremetal_bios_diff`, `acm_detect_violations`, `acm_diagnose_violation`, etc.).

## Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | Action to perform: `list`, `add`, `remove`, or `discover`. |
| `target` | string | Conditional | For `add`/`remove`: `secret_name/namespace` (e.g., `hub1/david`). For `discover`: the hub kubeconfig target key. |
| `cluster` | string | No | For `discover` only: name of a specific managed cluster to discover. When omitted, discovers all managed clusters from the hub. |

## Actions

### list

Returns all registered targets with their keys, sources, and timestamps.

**Request:**

```json
{ "action": "list" }
```

**Response:**

```json
{
  "count": 2,
  "targets": [
    {
      "key": "hub1/david",
      "source": "secret",
      "added_at": "2025-01-15T10:30:00Z"
    },
    {
      "key": "spoke1",
      "source": "discovered:hub1/david",
      "added_at": "2025-01-15T10:31:00Z"
    }
  ],
  "usage": "Pass a target key as the kubeconfig parameter to any tool."
}
```

### add

Registers a secret-backed target. The MCP server validates that the secret exists on its local cluster and contains a parseable kubeconfig before registering.

**Request:**

```json
{ "action": "add", "target": "hub1/david" }
```

The `target` format is `secret_name/namespace`. The secret must:
- Exist in the specified namespace on the MCP server's local cluster
- Contain a `kubeconfig` data key with valid kubeconfig YAML (base64-encoded in the Secret)

See [Target Cluster Secrets](target-cluster-secrets.md) for details on creating the secrets.

**Response:**

```json
{
  "status": "added",
  "key": "hub1/david",
  "usage": "Use kubeconfig='hub1/david' in any tool to target this cluster."
}
```

### remove

Unregisters a target. Does not delete the underlying secret.

**Request:**

```json
{ "action": "remove", "target": "hub1/david" }
```

**Response:**

```json
{
  "status": "removed",
  "key": "hub1/david"
}
```

### discover

Connects to an ACM hub cluster and extracts kubeconfigs for managed clusters. The hub is identified by an already-registered target key. Discovered clusters are registered by their cluster name (e.g., `spoke1`, `cnfdf04`).

**Discover all managed clusters:**

```json
{ "action": "discover", "target": "hub1/david" }
```

**Discover a specific cluster:**

```json
{ "action": "discover", "target": "hub1/david", "cluster": "spoke1" }
```

The tool:
1. Resolves the hub kubeconfig from the registered target
2. Verifies the target is an ACM hub (checks for `MultiClusterHub` CRD)
3. Lists `ManagedCluster` resources (skipping `local-cluster`)
4. For each cluster, extracts the admin kubeconfig from `ClusterDeployment` secrets (Hive) or falls back to a well-known secret name

**Response:**

```json
{
  "discovered": 3,
  "registered": 2,
  "failed": 1,
  "hub_target": "hub1/david",
  "clusters": [
    { "cluster": "spoke1", "status": "registered", "key": "spoke1" },
    { "cluster": "spoke2", "status": "registered", "key": "spoke2" },
    { "cluster": "spoke3", "status": "failed", "error": "kubeconfig secret not found..." }
  ],
  "usage": "Use the cluster name as the kubeconfig parameter in any tool (e.g. kubeconfig='spoke1')."
}
```

## Using Registered Targets

Once registered, pass the target key as the `kubeconfig` parameter to any tool:

```json
{
  "tool": "kube_compare_cluster_diff",
  "arguments": {
    "kubeconfig": "spoke1",
    "reference": "container://registry.redhat.io/openshift4/openshift-telco-core-rds-rhel9:v4.18:/..."
  }
}
```

```json
{
  "tool": "acm_detect_violations",
  "arguments": {
    "kubeconfig": "hub1/david"
  }
}
```

```json
{
  "tool": "baremetal_bios_diff",
  "arguments": {
    "kubeconfig": "hub1/david",
    "namespace": "my-cluster"
  }
}
```

## Typical Workflow

```bash
# 1. Create secrets for your cluster kubeconfigs (one-time setup)
oc create secret generic hub1 --from-file=kubeconfig=./hub-kubeconfig.yaml -n david

# 2. Register the hub with the MCP server
manage_targets(action="add", target="hub1/david")

# 3. Discover all managed clusters from the hub
manage_targets(action="discover", target="hub1/david")

# 4. Use any registered target with any tool
acm_detect_violations(kubeconfig="hub1/david")
acm_diagnose_violation(kubeconfig="hub1/david", policy_name="require-limits")
kube_compare_cluster_diff(kubeconfig="spoke1", reference="...")
kube_compare_validate_rds(kubeconfig="spoke1", rds_type="core")
baremetal_bios_diff(kubeconfig="hub1/david", namespace="my-cluster")
```

## Important Notes

- The target registry is **in-memory** and resets when the MCP server restarts. Targets must be re-registered after a restart.
- Secret-backed targets read the kubeconfig from the secret on each use, so credential rotations take effect immediately.
- Discovered targets store the kubeconfig in memory. If the managed cluster credentials rotate, re-run `discover`.
- The `discover` action requires the target to be an ACM hub cluster with `MultiClusterHub` installed.
- The MCP server must have RBAC permissions to read Secrets in the target namespace.

## Prerequisites

For the `add` action:
- The MCP server must be running inside a Kubernetes cluster (needs in-cluster config to read secrets)
- The secret must exist with a `kubeconfig` data key

For the `discover` action:
- The hub target must already be registered
- ACM must be installed on the hub cluster
- Hive `ClusterDeployment` resources must exist for the managed clusters (or well-known kubeconfig secrets)
