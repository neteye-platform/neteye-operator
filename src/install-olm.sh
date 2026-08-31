#!/bin/bash
set -euo pipefail
IFS=$'\n\t'

olmv1_manifest=https://github.com/operator-framework/operator-controller/releases/download/v1.11.0/operator-controller.yaml
olmv1_namespace=olmv1-system

usage() {
    cmd=$(basename $0)
    cat <<EOF
NAME
    ${cmd} - install OLMv1 into a cluster

SYNOPSIS
    ${cmd} [-n <namespace>] [-g <namespace/name>] [-h]

DESCRIPTION
    Installs OLMv1 in the provided <namespace> with cert-manager.
    A kubernetes configuration must already be present.

    -n <namespace>
        install OLMv1 in the given <namespace>. Defaults to olmv1-system.

    -g <namespace/name>
        configure OLM global pull secret by patching catalogd and
        operator-controller with --global-pull-secret=<namespace/name>.

    -h
        help (this text)
EOF
    exit 0
}


global_pull_secret=${GLOBAL_PULL_SECRET:-}

while getopts n:g:h opt; do
    case ${opt} in
        n) olmv1_namespace=${OPTARG} ;;
        g) global_pull_secret=${OPTARG} ;;
        h) usage ;;
        *) echo "Unknown option" >&2
           exit 1
    esac
done

if [[ -z "$olmv1_manifest" ]]; then
    echo "Error: Missing required MANIFEST variable"
    exit 1
fi

default_catalogs_manifest="https://github.com/operator-framework/operator-controller/releases/download/v1.11.0/default-catalogs.yaml"
cert_mgr_version=v1.18.2
install_default_catalogs=true
catalog_wait_timeout=${CATALOG_WAIT_TIMEOUT:-60s}

if [[ -z "$cert_mgr_version" ]]; then
    echo "Error: Missing CERT_MGR_VERSION variable"
    exit 1
fi

kubectl_wait() {
    namespace=$1
    runtime=$2
    timeout=$3

    kubectl wait --for=condition=Available --namespace="${namespace}" "${runtime}" --timeout="${timeout}"
}

kubectl_wait_rollout() {
    namespace=$1
    runtime=$2
    timeout=$3

    kubectl rollout status --namespace="${namespace}" "${runtime}" --timeout="${timeout}"
}

kubectl_wait_for_query() {
    manifest=$1
    query=$2
    timeout=$3
    poll_interval_in_seconds=$4

    if [[ -z "$manifest" || -z "$query" || -z "$timeout" || -z "$poll_interval_in_seconds" ]]; then
        echo "Error: Missing arguments."
        echo "Usage: kubectl_wait_for_query <manifest> <query> <timeout> <poll_interval_in_seconds>"
        exit 1
    fi

    start_time=$(date +%s)
    while true; do
        val=$(kubectl get "${manifest}" -o jsonpath="${query}" 2>/dev/null || echo "")
        if [[ -n "${val}" ]]; then
            echo "${manifest} has ${query}."
            break
        fi
        if [[ $(( $(date +%s) - start_time )) -ge ${timeout} ]]; then
            echo "Timed out waiting for ${manifest} to have ${query}."
            exit 1
        fi
        sleep ${poll_interval_in_seconds}s
    done
}

# Install cert-manager only if it is not already present on the cluster.
# Check both the CRD and the controller deployments to avoid false positives
# from stale CRDs left behind by a partial or incomplete installation.
if kubectl get crd certificates.cert-manager.io &>/dev/null && \
   kubectl get deployment -n cert-manager cert-manager-webhook &>/dev/null && \
   kubectl get deployment -n cert-manager cert-manager-cainjector &>/dev/null; then
    echo "cert-manager is already installed, skipping installation"
else
    kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/${cert_mgr_version}/cert-manager.yaml"
fi
# Wait for cert-manager to be fully ready
kubectl_wait "cert-manager" "deployment/cert-manager-webhook" "60s"
kubectl_wait "cert-manager" "deployment/cert-manager-cainjector" "60s"
kubectl_wait "cert-manager" "deployment/cert-manager" "60s"
kubectl_wait_for_query "mutatingwebhookconfigurations/cert-manager-webhook" '{.webhooks[0].clientConfig.caBundle}' 60 5
kubectl_wait_for_query "validatingwebhookconfigurations/cert-manager-webhook" '{.webhooks[0].clientConfig.caBundle}' 60 5

patch_global_pull_secret_arg() {
    namespace=$1
    deployment=$2
    secret_ref=$3

    args=$(kubectl -n "${namespace}" get deployment "${deployment}" -o jsonpath='{.spec.template.spec.containers[0].args[*]}' 2>/dev/null || true)
    if [[ " ${args} " == *" --global-pull-secret=${secret_ref} "* ]]; then
        echo "${deployment} already configured with --global-pull-secret=${secret_ref}"
        return 0
    fi

    kubectl -n "${namespace}" patch deployment "${deployment}" --type=json -p "$(cat <<EOF
[
  {"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--global-pull-secret=${secret_ref}"}
]
EOF
)"
    echo "patched ${deployment} with --global-pull-secret=${secret_ref}"
}

# Change the file into a file:// url
if [ -f "${olmv1_manifest}" ]; then
    olmv1_manifest=file://localhost$(realpath ${olmv1_manifest})
fi

# Clean up old RBAC resources from previous releases. The ClusterRoleBinding was
# renamed and the custom ClusterRole replaced with cluster-admin.
kubectl delete clusterrolebinding operator-controller-manager-rolebinding operator-controller-manager-admin-rolebinding --ignore-not-found
kubectl delete clusterrole operator-controller-manager-role --ignore-not-found

curl -L -s "${olmv1_manifest}" | sed "s/olmv1-system/${olmv1_namespace}/g" | kubectl apply -f -
# Wait for the rollout, and then wait for the deployment to be Available
kubectl_wait_rollout "${olmv1_namespace}" "deployment/catalogd-controller-manager" "60s"
kubectl_wait "${olmv1_namespace}" "deployment/catalogd-controller-manager" "60s"
kubectl_wait "${olmv1_namespace}" "deployment/operator-controller-controller-manager" "60s"

if [[ -n "${global_pull_secret}" ]]; then
    if [[ "${global_pull_secret}" != */* ]]; then
        echo "Error: -g must be in <namespace>/<secret-name> format"
        exit 1
    fi

    patch_global_pull_secret_arg "${olmv1_namespace}" "catalogd-controller-manager" "${global_pull_secret}"
    patch_global_pull_secret_arg "${olmv1_namespace}" "operator-controller-controller-manager" "${global_pull_secret}"

    kubectl_wait_rollout "${olmv1_namespace}" "deployment/catalogd-controller-manager" "60s"
    kubectl_wait_rollout "${olmv1_namespace}" "deployment/operator-controller-controller-manager" "60s"
fi

if [[ "${install_default_catalogs}" != "false" ]]; then
    kubectl apply -f "${default_catalogs_manifest}"
    kubectl wait --for=condition=Serving "clustercatalog/operatorhubio" --timeout="${catalog_wait_timeout}"
fi