# Architecture

This document describes how kube-compare-mcp and openshift-mcp-server integrate with OpenShift Lightspeed (OLS) to provide ACM policy debugging and cluster compliance capabilities.

## System Overview

```
┌──────────────────────────────────────────────────────────────────────┐
│  Hub OpenShift Cluster                                               │
│                                                                      │
│  ┌────────────────────────────────────────────────────────────┐      │
│  │  OLS Pod (openshift-lightspeed namespace)                  │      │
│  │                                                            │      │
│  │  ┌───────────────────┐  ┌──────────────────────────────┐  │      │
│  │  │ OLS API Server    │  │ openshift-mcp-server         │  │      │
│  │  │ (port 8443)       │  │ (port 8080)                  │  │      │
│  │  │                   │  │                              │  │      │
│  │  │ System Prompt     │  │ Multi-cluster resource       │  │      │
│  │  │ + RAG Context     │──│ access, OLM tracing          │  │      │
│  │  │ + Tool Calls      │  │                              │  │      │
│  │  └────────┬──────────┘  └──────────────────────────────┘  │      │
│  └───────────┼────────────────────────────────────────────────┘      │
│              │                                                       │
│  ┌───────────┼────────────────────────────────────────────────┐      │
│  │  kube-compare-mcp Pod (kube-compare-mcp namespace)         │      │
│  │  │                                                         │      │
│  │  │  ┌──────────────────────────────────────────────────┐   │      │
│  │  │  │ kube-compare-mcp (port 8080)                     │   │      │
│  │  │  │ 6 MCP Tools: comparison + ACM diagnostics        │   │      │
│  │  └──┴──────────────────────┬───────────────────────────┘   │      │
│  └────────────────────────────┼───────────────────────────────┘      │
│                               │                                      │
│  ┌────────────────────────────▼───────────────────────────────┐      │
│  │  Kubernetes API Server + ACM Hub Resources                 │      │
│  └────────────────────────────┬───────────────────────────────┘      │
└───────────────────────────────┼──────────────────────────────────────┘
                                │ (ACM cluster-proxy)
                    ┌───────────▼───────────┐
                    │  Managed Clusters      │
                    │  (spokes via ACM)      │
                    └───────────────────────┘
```

## Two MCP Servers

### kube-compare-mcp (hub-only)

Deployed as a standalone pod in its own namespace. Accesses only the hub cluster via in-cluster service account. Provides:

- **Comparison tools**: Cluster diff, RDS resolution/validation, BIOS diff
- **ACM diagnostic tools**: Policy inspection and diagnosis using hub-side policy data

### openshift-mcp-server (multi-cluster)

Deployed alongside OLS. Uses ACM cluster-proxy for managed cluster access. Provides:

- **Resource access**: `resources_get`, `resources_list` with `cluster` parameter for spoke access
- **OLM tracing**: `trace_olm_subscription` to walk OLM chains on managed clusters
- **Pod operations**: `pods_log`, pod listing, exec
- **Cluster management**: `cluster_list`, namespace listing

## kube-compare-mcp Tools

| Tool | Description |
|------|-------------|
| `kube_compare_cluster_diff` | Compare cluster state against a Reference Design Specification |
| `kube_compare_resolve_rds` | Resolve available Reference Design Specifications |
| `kube_compare_validate_rds` | Validate an RDS against a cluster |
| `baremetal_bios_diff` | Compare bare metal BIOS settings against reference |
| [`inspect_acm_policy`](tool-inspect-acm-policy.md) | Extract structured violation details from an ACM Policy |
| [`diagnose_acm_policy`](tool-diagnose-acm-policy.md) | Classify ACM policy violations and suggest next tool calls |

## Diagnostic Workflow

A typical ACM policy diagnosis follows this flow:

```
1. diagnose_acm_policy(policy_name) [kube-compare-mcp]
   → Reads hub-side policy data
   → Classifies violations: resource_missing, resource_drift, olm_stuck
   → Returns suggested_tool_call for each issue

2. For OLM issues:
   trace_olm_subscription(subscription_name, cluster=spoke) [openshift-mcp-server]
   → Walks Subscription → CatalogSource → InstallPlan → CSV
   → Identifies the exact failing step

3. For resource issues:
   resources_get(resource, namespace, cluster=spoke) [openshift-mcp-server]
   → Retrieves actual resource state from managed cluster
   → Compare with desired state from diagnosis output

4. LLM classifies root cause using evidence
   → Presents findings and suggested remediation
```

## RAG Pipeline

### Content

The `rag-content/` directory contains 4 minimal ACM-focused markdown documents:

| File | Purpose |
|------|---------|
| `acm-architecture.md` | Hub/spoke model, tool architecture, decision tree |
| `tool-diagnose-acm-policy.md` | Primary entry point for ACM debugging |
| `tool-inspect-acm-policy.md` | Quick policy status extraction |
| `playbook-acm-debugging.md` | Unified debugging playbook with error interpretation |

### Build Process

```bash
# Generate embeddings from rag-content/ using ragtool container
podman run --rm \
  -v ./rag-content:/markdown:Z \
  -v /tmp/rag-output:/output:Z \
  kube-compare-mcp:ragtool

# Build RAG data image
podman build -t kube-compare-mcp:rag -f /tmp/Dockerfile.rag /tmp/

# Push to registry
podman push kube-compare-mcp:rag <registry>/kube-compare-mcp:rag
```

### OLS Configuration

```yaml
apiVersion: ols.openshift.io/v1alpha1
kind: OLSConfig
metadata:
  name: cluster
spec:
  featureGates:
    - MCPServer
  mcpServers:
    - name: openshift-mcp-server
      streamableHTTP:
        url: http://kubernetes-mcp-server.openshift-lightspeed.svc:8080/mcp
    - name: kube-compare-mcp
      streamableHTTP:
        url: http://kube-compare-mcp.kube-compare-mcp.svc.cluster.local:8080/mcp
        timeout: 60
  ols:
    rag:
      - image: <registry>/kube-compare-mcp:rag
        indexID: vector_db_index
        indexPath: /rag/vector_db
```

## RBAC

kube-compare-mcp needs read access to ACM resources on the hub:

- `policies.policy.open-cluster-management.io` (get, list)
- `configurationpolicies.policy.open-cluster-management.io` (get, list)
- Standard Kubernetes resources for comparison tools

openshift-mcp-server handles managed cluster access via ACM cluster-proxy. The `admin` user on managed clusters needs read-only access, deployed via ACM policy with a custom `ClusterRole` granting `get`, `list`, `watch` on all resources.

## Security

- kube-compare-mcp uses in-cluster config only for ACM tools (no external kubeconfig needed)
- Comparison tools accept kubeconfig for remote cluster comparison, with exec-auth and auth-provider blocking
- Kubeconfig size limited to 1MB encoded / 768KB decoded
- Sensitive information redacted from error messages
