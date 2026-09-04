podman build -t neteye-keycloak-authflow-controller:latest .
podman tag neteye-keycloak-authflow-controller:latest   172.19.69.253:5000/neteye-keycloak-authflow-controller:latest
podman push --tls-verify=false   172.19.69.253:5000/neteye-keycloak-authflow-controller:latest