# k3s bootstrap recipe

A concise guide to joining [k3s](https://k3s.io) agent (worker) nodes with
karpenter-provider-hetzner.

See [`examples/k3s-nodeclass.yaml`](../examples/k3s-nodeclass.yaml) for an
apply-ready NodeClass and NodePool with a full cloud-init template.

> Karpenter is **bootstrap-agnostic** — Hetzner runs the `userData` string as
> cloud-init on first boot, so the same provider that joins kubeadm or Talos
> nodes joins k3s agents. Only the cloud-init contents differ.

---

## How it works

The cloud-init in `examples/k3s-nodeclass.yaml`:

1. Writes `/etc/rancher/k3s/config.yaml` with `kubelet-arg: cloud-provider=external`
   so the hcloud cloud controller manager (not k3s) owns the node lifecycle and
   sets the `hcloud://<id>` providerID Karpenter relies on.
2. Runs the official k3s installer in **agent** mode, pointing at the server's
   `:6443` endpoint with the cluster's node-join token.

The node appears in `kubectl get nodes` within two to three minutes of the
Hetzner server becoming active.

---

## Prerequisites

| Requirement | Why |
|--|--|
| k3s server reachable on `:6443` | The agent registers against the supervisor/API port. |
| k3s node-join token | Authenticates the agent. `sudo cat /var/lib/rancher/k3s/server/node-token` on a server. |
| hcloud-cloud-controller-manager installed | Assigns `providerID: hcloud://<id>`, which Karpenter uses to map a NodeClaim to its Node. |
| k3s servers started with `--disable-cloud-controller` | Hands node lifecycle to the hcloud CCM instead of k3s's built-in one. |

> **Name matching:** the hcloud CCM matches a Node to a Hetzner server by name.
> Hetzner sets the node hostname to the server name (which the provider
> controls), so this matches out of the box. If you override `node-name`, keep
> it equal to the server name.

---

## Step 1: get the node-join token

On an existing k3s server:

```bash
sudo cat /var/lib/rancher/k3s/server/node-token
# K10<hash>::server:<secret>
```

This token is long-lived. Treat it as a cluster credential — see "Keeping
secrets out of git" below.

---

## Step 2: fill in the NodeClass

Replace the placeholders in `examples/k3s-nodeclass.yaml`:

```yaml
userData: |
  #cloud-config
  runcmd:
    - |
      curl -sfL https://get.k3s.io | \
        K3S_URL="https://<CONTROL_PLANE_ENDPOINT>:6443" \
        K3S_TOKEN="<NODE_TOKEN>" \
        INSTALL_K3S_VERSION="v1.31.5+k3s1" \
        sh -s - agent
```

Pin `INSTALL_K3S_VERSION` to match your server's k3s version (`k3s --version`)
so agents don't run a newer minor than the control plane.

### Private-network data plane

If your cluster routes pod/node traffic over the Hetzner private network,
uncomment `node-ip` / `flannel-iface` in the NodeClass `config.yaml` block and
set them to the node's private IP and NIC (commonly `enp7s0` on Hetzner). This
keeps kubelet and flannel off the public interface.

### Keeping secrets out of git

The node-join token grants cluster membership. Store the full cloud-init blob in
a Secret and reference it with `userDataSecretRef` instead of inlining it:

```bash
kubectl create secret generic k3s-worker-userdata \
  --namespace kube-system \
  --from-file=cloud-init.yaml=/path/to/cloud-init.yaml
```

```yaml
# In the NodeClass spec — remove userData and add:
userDataSecretRef:
  namespace: kube-system
  name: k3s-worker-userdata
  key: cloud-init.yaml
```

`userDataSecretRef` takes precedence over `userData` when both are set.

---

## Step 3: apply and verify

```bash
kubectl apply -f examples/k3s-nodeclass.yaml

# NodeClass should report Ready:
kubectl get hcloudnodeclasses k3s-default

# Watch for new agents joining:
kubectl get nodes -w
```

If a node does not join within five minutes, SSH in (while `enablePublicIPv4`
is `true`, or via the Hetzner console) and inspect the logs:

```bash
cat /var/log/cloud-init-output.log
journalctl -u k3s-agent --no-pager | tail -40
```

A node stuck with the `node.cloudprovider.kubernetes.io/uninitialized` taint
means the hcloud CCM has not initialized it — check the CCM is running and that
the node hostname matches the Hetzner server name.

---

## Trade-offs vs kubeadm and Talos

| | k3s | Ubuntu + kubeadm | Talos |
|--|--|--|--|
| Custom image required | No (Ubuntu public image) | No | Yes |
| Install complexity | Lowest (single installer) | Medium | Medium (machineconfig) |
| Footprint | Small (single binary) | Full kubeadm stack | Minimal immutable OS |
| Bootstrap secret | Long-lived node-token | Short-lived kubeadm token | Machineconfig CA + token |
| Best fit | Edge, small/medium clusters, k3s shops | Mixed/general workloads | Production, security-focused |

For k3s-based installers (e.g. hetzner-k3s, kube-hetzner) this is the natural
path; reuse your existing cluster's k3s token and server endpoint.
