podman build -t neteye-keycloak-realm-controller:latest .
podman tag neteye-keycloak-realm-controller:latest  172.19.69.253:5000/neteye-keycloak-realm-controller:latest
podman push --tls-verify=false   172.19.69.253:5000/neteye-keycloak-realm-controller:latest