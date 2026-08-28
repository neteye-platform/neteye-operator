TOKEN=$(
  curl --silent -X POST   "https://rdneteye.si.wp.lan/auth/realms/master/protocol/openid-connect/token"   -H "Content-Type: application/x-www-form-urlencoded"   -d "client_id=admin-cli"   -d "username=neteye-internal-keycloak-admin"   -d "password=orSM7tpBz3o2KTcmVQdiKWcvuJY0YUz2"   -d "grant_type=password" | jq -r '.access_token'
)

for id in \
  6bf2627b-2088-4337-986f-7a336dadaef3 \
  736d2093-d185-4f12-9c02-1fb7c410ef8e \
  cd08a96d-b5cf-455b-829a-1b4cf7780d80 \
  c7ed5579-812e-4a1f-bc5b-e0133e9e13c9
do
  echo "===== $id ====="
  curl --silent -k \
    -H "Authorization: Bearer $TOKEN" \
    "https://rdneteye.si.wp.lan/auth/admin/realms/master/authentication/config/$id" | jq
done