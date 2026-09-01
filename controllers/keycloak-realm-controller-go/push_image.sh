podman build -t neteye-keycloak-realm-controller-go:latest .
podman tag neteye-keycloak-realm-controller-go:latest   127.0.0.1:5000/neteye-keycloak-realm-controller-go:latest
podman push --tls-verify=false   127.0.0.1:5000/neteye-keycloak-realm-controller-go:latest