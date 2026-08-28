TOKEN=$(
  curl --silent -X POST   "https://rdneteye.si.wp.lan/auth/realms/master/protocol/openid-connect/token"   -H "Content-Type: application/x-www-form-urlencoded"   -d "client_id=admin-cli"   -d "username=neteye-internal-keycloak-admin"   -d "password=orSM7tpBz3o2KTcmVQdiKWcvuJY0YUz2"   -d "grant_type=password" | jq -r '.access_token'
)

curl -s -X GET "https://rdneteye.si.wp.lan/auth/admin/realms/master/authentication/flows" \
  -H "Authorization: Bearer $TOKEN" \
  | jq '.[] | select(.alias=="neteye-idp-discovery-flow")'