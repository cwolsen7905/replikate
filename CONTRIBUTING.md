# Contributing to Replikate

Thanks for your interest in improving Replikate! This project is a small,
focused Kubernetes controller, and contributions of all sizes are welcome.

## Getting started

Requirements: Go 1.23+, and (for image/deploy work) Docker with buildx/QEMU and
Helm. A Kubernetes cluster — [kind](https://kind.sigs.k8s.io/) works well — is
handy for trying changes end to end.

```sh
git clone https://github.com/cwolsen7905/replikate
cd replikate
make tidy             # resolve dependencies
make build            # compile ./bin/replikate
make test             # run the unit test suite
make test-integration # run envtest-backed integration tests (downloads control-plane binaries)
make run              # run against your current kubeconfig (out-of-cluster)
```

`make test-integration` runs the real manager against a throwaway API server
via [envtest](https://book.kubebuilder.io/reference/envtest.html); it fetches
the `kube-apiserver`/`etcd` binaries into `./bin` on first run. The integration
tests live behind the `integration` build tag, so plain `make test` stays fast
and needs no extra binaries.

`make smoke-test` runs the validating webhook end to end on a real
[kind](https://kind.sigs.k8s.io/) cluster with cert-manager — the one path
envtest can't model. It needs `kind`, `docker`, `helm`, and `kubectl`; CI also
runs it (`.github/workflows/webhook-smoke.yaml`) on webhook/chart changes.

## Development workflow

1. Create a topic branch off `main`.
2. Make your change. Keep it focused — one logical change per pull request.
3. Run `make fmt vet test build` and make sure everything passes.
4. Update `CHANGELOG.md` under the `[Unreleased]` section, and `ROADMAP.md` if
   your change affects the plan.
5. Open a pull request against `main` describing the what and the why.

CI (GitHub Actions) runs `go vet`, `go build`, and `go test` on every pull
request; please make sure it's green.

## Commit and PR conventions

- Write imperative, present-tense commit subjects ("Add namespace exclusions"),
  with a body explaining the reasoning when it isn't obvious.
- Reference related issues in the PR description.
- Prefer small, reviewable PRs over large ones.

## Code style

- Standard Go formatting (`gofmt` / `go fmt ./...`).
- Match the surrounding code's naming and comment density.
- Keep the controller logic in `internal/controller` and the entrypoint in
  `cmd`. Pure helpers stay dependency-free and easy to test.

## Reporting bugs and requesting features

Open a [GitHub issue](https://github.com/cwolsen7905/replikate/issues) with
enough detail to reproduce (Kubernetes version, controller version, the source
object's annotations, and what you expected vs. what happened).

## License

By contributing, you agree that your contributions will be licensed under the
project's [BSD 3-Clause License](LICENSE).
