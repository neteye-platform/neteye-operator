# Third-party CRDs, for tests only

The operator drives three APIs it does not own: the upstream Keycloak
Operator's `k8s.keycloak.org/Keycloak`, OLM's
`olm.operatorframework.io/ClusterExtension`, and cert-manager's `Certificate`
and `Issuer`. None is installed in envtest, and none has its Go types compiled
in.

These are stand-ins carrying only what the operator actually reads and writes,
with the rest left unvalidated. They exist so the deployment stages can be
exercised against a real API server; they are not a copy of the upstream
schemas and must never be applied to a real cluster.
