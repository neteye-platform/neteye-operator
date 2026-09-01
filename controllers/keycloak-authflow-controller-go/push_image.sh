podman build -t neteye-keycloak-authflow-controller-go:latest .
podman tag neteye-keycloak-authflow-controller-go:latest   127.0.0.1:5000/neteye-keycloak-authflow-controller-go:latest
podman push --tls-verify=false   127.0.0.1:5000/neteye-keycloak-authflow-controller-go:latest