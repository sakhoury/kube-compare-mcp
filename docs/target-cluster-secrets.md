# Target Cluster Secrets

The MCP server can connect to remote clusters by reading kubeconfig data stored as
Kubernetes Secrets on the local cluster where the server is running. This document
describes the expected Secret format and how to register targets with the
`manage_targets` tool.

## Secret Format

Each target cluster kubeconfig must be stored as an **Opaque** Secret with a single
data key named **`kubeconfig`** containing a valid kubeconfig YAML file.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: <secret-name>
  namespace: <namespace>
type: Opaque
data:
  kubeconfig: <base64-encoded kubeconfig YAML>
```

### Required Fields

| Field | Description |
|-------|-------------|
| `metadata.name` | A descriptive name for the target cluster (e.g. `hub1`, `spoke1`) |
| `metadata.namespace` | The namespace where the secret lives (e.g. `david`) |
| `data.kubeconfig` | Base64-encoded kubeconfig YAML with valid credentials |

The kubeconfig inside the secret can use any supported authentication method:
token-based auth, client certificate auth, etc.

## Creating Secrets

### From a kubeconfig file

```bash
oc create secret generic <secret-name> \
  --from-file=kubeconfig=<path-to-kubeconfig-file> \
  -n <namespace>
```

**Examples:**

```bash
# Hub cluster kubeconfig (token auth)
oc create secret generic hub1 \
  --from-file=kubeconfig=./config.test \
  -n david

# Spoke/managed cluster kubeconfig (client cert auth)
oc create secret generic spoke1 \
  --from-file=kubeconfig=~/Downloads/kubeconfig.yaml \
  -n david
```

### From inline YAML

```bash
oc create -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: hub1
  namespace: david
type: Opaque
stringData:
  kubeconfig: |
    apiVersion: v1
    kind: Config
    clusters:
    - cluster:
        server: https://api.mycluster.example.com:6443
        insecure-skip-tls-verify: true
      name: mycluster
    contexts:
    - context:
        cluster: mycluster
        user: admin
      name: mycluster
    current-context: mycluster
    users:
    - name: admin
      user:
        token: sha256~your-token-here
EOF
```

> **Note:** When using `stringData`, Kubernetes automatically base64-encodes the
> value. When using `data`, you must base64-encode it yourself.

## Registering Targets with the MCP Server

Once the secrets exist on the cluster, register them with the MCP server using the
`manage_targets` tool.

### Add a target

```json
{
  "action": "add",
  "target": "hub1/david"
}
```

The `target` format is `secret_name/namespace`. The server validates the secret
exists and contains a parseable kubeconfig. On success, it returns a **target key**
(e.g. `hub1/david`).

### List registered targets

```json
{
  "action": "list"
}
```

Returns all registered targets with their keys.

### Remove a target

```json
{
  "action": "remove",
  "target": "hub1/david"
}
```

### Using a registered target

Pass the target key as the `kubeconfig` parameter to any tool:

```json
{
  "kubeconfig": "hub1/david",
  "policy_name": "my-policy"
}
```

The server detects the `secret_name/namespace` format, reads the secret from the
local cluster, and connects to the remote cluster using the kubeconfig inside.

## Example Setup

```bash
# 1. Create a namespace for target secrets
oc create namespace david

# 2. Store hub cluster kubeconfig
oc create secret generic hub1 \
  --from-file=kubeconfig=./hub-kubeconfig.yaml \
  -n david

# 3. Store managed/spoke cluster kubeconfig
oc create secret generic spoke1 \
  --from-file=kubeconfig=./spoke-kubeconfig.yaml \
  -n david

# 4. Verify secrets
oc get secrets -n david
# NAME     TYPE     DATA   AGE
# hub1     Opaque   1      5s
# spoke1   Opaque   1      3s
```

Then in the MCP tool conversation:

1. Register: `manage_targets(action="add", target="hub1/david")`
2. Use: `acm_detect_violations(kubeconfig="hub1/david")`
3. Diagnose: `acm_diagnose_violation(kubeconfig="hub1/david", policy_name="my-policy")`
4. Diff: `kube_compare_cluster_diff(kubeconfig="hub1/david", reference="...")`

## Notes

- The target registry is **in-memory** and resets when the MCP server restarts.
  Targets must be re-registered after a restart.
- The `kubeconfig` data key name is required and case-sensitive.
- The MCP server must have RBAC permissions to read Secrets in the target namespace.
- Kubeconfig credentials (tokens, certificates) must be valid and not expired.
