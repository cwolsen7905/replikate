# replikate

[![CI](https://github.com/cwolsen7905/replikate/actions/workflows/ci.yaml/badge.svg)](https://github.com/cwolsen7905/replikate/actions/workflows/ci.yaml)
[![Release](https://img.shields.io/github/v/release/cwolsen7905/replikate?sort=semver)](https://github.com/cwolsen7905/replikate/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/cwolsen7905/replikate)](https://goreportcard.com/report/github.com/cwolsen7905/replikate)
[![License: BSD-3-Clause](https://img.shields.io/badge/License-BSD--3--Clause-blue.svg)](LICENSE)

Replikate is a lightweight, BSD-licensed Kubernetes controller that replicates ConfigMaps and Secrets across namespaces. Annotate a source object and Replikate copies it into every matching namespace — selected by labels or cluster-wide — keeping copies in sync on change, cleaning them up on delete, and populating new namespaces automatically.

## How it works

Annotate a ConfigMap or Secret with `replikate.brainchurts.com/sync`. The controller then maintains a managed copy of it in every namespace you select:

- **Value = a label selector** (e.g. `team=web`) → copies land in namespaces whose labels match.
- **Value = empty string** (`""`) → copies land in every namespace.

Replikate keeps copies in lockstep with the source:

- Source changed → every copy is updated.
- Source deleted, or the annotation removed → copies are deleted (via a finalizer).
- A new namespace appears that matches the selector → it is populated automatically.

Each copy is stamped with `replikate.brainchurts.com/managed-by=replikate` plus origin labels. Replikate will **never overwrite an object it does not manage** — with one deliberate exception: copies left behind by AppsCode's config-syncer (see [Migrating from config-syncer](#migrating-from-config-syncer)). Every action it takes (create / update / adopt / delete a copy) is logged, and it only writes when something actually changed, so the controller logs stay meaningful.

## Quick start

Images are published multi-arch (`linux/amd64` + `linux/arm64`) to `ghcr.io/cwolsen7905/replikate` by CI on every push to `main`.

```sh
# 1. Install into the cluster (image is built by CI; or `make docker-build docker-push`):
make deploy IMAGE_REPO=ghcr.io/cwolsen7905/replikate IMAGE_TAG=v0.1.0
#   (equivalently: helm upgrade --install replikate charts/replikate \
#      --namespace replikate --create-namespace \
#      --set image.tag=v0.1.0)

# 2. Try it:
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

## Migrating from config-syncer

Replikate is annotation-compatible with AppsCode's config-syncer (kubed): the value of the `sync` annotation is a namespace label selector with the same meaning. To migrate:

1. Deploy Replikate alongside config-syncer.
2. Scale config-syncer to zero so it stops reacting to the annotation change.
3. Rename the annotation on your sources from `kubed.appscode.com/sync` to `replikate.brainchurts.com/sync`.
4. Remove config-syncer once Replikate is managing the copies.

Replikate **adopts config-syncer's existing copies in place** — any object carrying config-syncer's `kubed.appscode.com/origin` marker is relabeled and taken over rather than refused, so replicated data (for example, TLS secrets) is never deleted and recreated during the cutover.

## Development

```sh
make tidy     # resolve deps (writes go.mod/go.sum)
make build    # compile ./bin/replikate
make test     # go test ./...
make run      # run against your current kubeconfig (out-of-cluster)
```

Requires Go 1.23+, and Docker (with buildx/QEMU for multi-arch) + Helm for the image/deploy targets. The container is a `distroless/static:nonroot` image; RBAC and the Deployment ship in `charts/replikate`.

## Scope & limitations

- Replicates **ConfigMaps and Secrets** by annotation, **intra-cluster only**. Cross-cluster sync is a planned follow-up (see [ROADMAP.md](ROADMAP.md)).
- A manually-edited or deleted replica is restored **near-instantly** — managed copies are watched, not left until the next resync.
- If two sources with the same name in different namespaces target the same namespace, the first to create the copy keeps it: Replikate refuses to overwrite a copy owned by another source and emits a `Conflict` event, rather than the two fighting over it.

## License

BSD 3-Clause. See [LICENSE](LICENSE).
