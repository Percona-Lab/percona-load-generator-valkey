#!/bin/sh
# Generate throwaway certificates for the TLS test server (make valkey-tls-up).
# A CA, a server certificate for localhost / 127.0.0.1, and a client certificate.
# Output: .test-certs/ (ignored by git). Never use these anywhere but tests.
set -eu
dir="${1:-.test-certs}"
mkdir -p "$dir"
cd "$dir"
if [ -f ca.crt ] && [ -f server.crt ] && [ -f client.crt ]; then exit 0; fi
openssl genrsa -out ca.key 2048 2>/dev/null
openssl req -x509 -new -nodes -key ca.key -sha256 -days 30 -subj "/CN=plgv-test-ca" -out ca.crt
cat > server.ext <<EOT
subjectAltName = DNS:localhost, DNS:valkey-tls, IP:127.0.0.1
extendedKeyUsage = serverAuth
EOT
cat > client.ext <<EOT
extendedKeyUsage = clientAuth
EOT
openssl genrsa -out server.key 2048 2>/dev/null
openssl req -new -key server.key -subj "/CN=localhost" -out server.csr
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out server.crt -days 30 -sha256 -extfile server.ext 2>/dev/null
openssl genrsa -out client.key 2048 2>/dev/null
openssl req -new -key client.key -subj "/CN=plgv-client" -out client.csr
openssl x509 -req -in client.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out client.crt -days 30 -sha256 -extfile client.ext 2>/dev/null
# A second CA and client certificate the server does not trust (negative tests).
openssl genrsa -out other-ca.key 2048 2>/dev/null
openssl req -x509 -new -nodes -key other-ca.key -sha256 -days 30 -subj "/CN=other-ca" -out other-ca.crt
openssl genrsa -out other-client.key 2048 2>/dev/null
openssl req -new -key other-client.key -subj "/CN=intruder" -out other-client.csr
openssl x509 -req -in other-client.csr -CA other-ca.crt -CAkey other-ca.key -CAcreateserial -out other-client.crt -days 30 -sha256 -extfile client.ext 2>/dev/null
chmod 644 *.crt *.key
rm -f *.csr *.srl *.ext
