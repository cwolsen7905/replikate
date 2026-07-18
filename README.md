# replikate

Replikate is a lightweight, BSD-licensed Kubernetes controller that replicates ConfigMaps and Secrets across namespaces. Annotate a source object and Replikate copies it into every matching namespace — selected by labels or cluster-wide — keeping copies in sync on change, cleaning them up on delete, and populating new namespaces automatically.

## How it works

Annotate a ConfigMap or Secret with `replikate.brainchurts.com/sync`. The controller then maintains a managed copy of it in every namespace you select:

- **Value = a label selector** (e.g. `team=web`) → copies land in namespaces whose labels match.
- **Value = empty string** (`""`) → copies land in every namespace.

Replikate keeps copies in lockstep with the source:

- Source changed → every copy is updated.
- Source deleted, or the annotation removed → copies are deleted (via a finalizer).
- A new namespace appears that matches the selector → it is populated automatically.

Each copy is stamped with `replikate.brainchurts.com/managed-by=replikate` plus origin labels. Replikate will **never overwrite an object it does not manage**, so it won't clobber a ConfigMap/Secret you created by hand.

## Quick start

```sh
# 1. Push an image (CI does this automatically on push to main), or build it:
make docker-build docker-push IMAGE_REPO=ghcr.io/cwolsen7905/replikate IMAGE_TAG=v0.1.0

# 2. Install into the cluster:
make deploy IMAGE_REPO=ghcr.io/cwolsen7905/replikate IMAGE_TAG=v0.1.0
#   (equivalently: helm upgrade --install replikate charts/replikate \
#      --namespace replikate-system --create-namespace \
#      --set image.tag=v0.1.0)

# 3. Try it:
kubectl apply -f examples/example-configmap.yaml
kubectl label namespace some-namespace team=web        # gets a copy
kubectl get configmap shared-config -n some-namespace  # the replica
```

## Example

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: shared-config
  namespace: default
  annotations:
    replikate.brainchurts.com/sync: "team=web"   # "" for all namespaces
data:
  LOG_LEVEL: "info"
```

## Development

```sh
make tidy     # resolve deps (writes go.mod/go.sum)
make build    # compile ./bin/replikate
make test     # go test ./...
make run      # run against your current kubeconfig (out-of-cluster)
```

Requires Go 1.23+, and Docker + Helm for the image/deploy targets. The container is a `distroless/static:nonroot` image; RBAC and the Deployment ship in `charts/replikate`.

## Scope & limitations (MVP)

- Replicates **ConfigMaps and Secrets** by annotation, intra-cluster only. Cross-cluster sync is a planned follow-up.
- A manually-edited replica is corrected on the next source change or the controller's periodic resync, not instantly.
- If two sources with the same name in different namespaces target the same namespace, their copies collide on name; give such sources distinct names.

## License

BSD 3-Clause. See [LICENSE](LICENSE).
