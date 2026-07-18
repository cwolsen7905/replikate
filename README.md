# replikate
Replikate is a lightweight, BSD-licensed Kubernetes controller that replicates ConfigMaps and Secrets across namespaces. Annotate a source object and Replikate copies it into every matching namespace — selected by labels or cluster-wide — keeping copies in sync on change, cleaning them up on delete, and populating new namespaces automatically
