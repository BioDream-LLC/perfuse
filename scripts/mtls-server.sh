#!/usr/bin/env bash
# Starts an nginx that demands a client certificate, for the mutual TLS tests in internal/engine.
#
# Exists because those tests are the only place client certificates are checked against something other than Go. Everywhere else it is
# crypto/tls talking to crypto/tls, which proves the two halves agree and nothing about what OpenSSL makes of what we send. That
# distinction is not academic here: the SAML canonicaliser passed a thousand lines of self-agreeing tests while being unable to accept
# any assertion a real identity provider produced.
#
# Also the groundwork for TEFCA, where mutual TLS is the transport rather than an option.
set -euo pipefail

# Under $HOME rather than /tmp, because the container runtime mounts the home directory and not /tmp - a bind mount of a file under
# /tmp fails with "not a directory", which reads like a permissions problem and is not one.
DIR="$HOME/mtls-test"
PORT=${MTLS_PORT:-9443}

rm -rf "$DIR"
mkdir -p "$DIR"
cd "$DIR"

echo "== certificates"
openssl req -x509 -newkey rsa:2048 -nodes -keyout ca.key -out ca.crt -days 2 \
  -subj "/CN=Perfuse Test CA" 2>/dev/null

openssl req -newkey rsa:2048 -nodes -keyout server.key -out server.csr \
  -subj "/CN=receiver.test" 2>/dev/null

# The tests connect to 127.0.0.1, so the address has to be in the certificate. Without it the handshake fails for a reason that looks
# like a configuration error in Perfuse.
printf "subjectAltName=DNS:receiver.test,IP:127.0.0.1\n" > san.ext

openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out server.crt -days 2 -extfile san.ext 2>/dev/null

openssl req -newkey rsa:2048 -nodes -keyout client.key -out client.csr \
  -subj "/CN=perfuse-client" 2>/dev/null

openssl x509 -req -in client.csr -CA ca.crt -CAkey ca.key -out client.crt -days 2 2>/dev/null

echo "== nginx configuration"
cat > nginx.conf <<'EOF'
events {}
http {
  access_log /dev/stdout;
  error_log  /dev/stdout info;

  server {
    listen 8443 ssl;
    server_name receiver.test;

    ssl_certificate     /certs/server.crt;
    ssl_certificate_key /certs/server.key;

    # The point of the exercise. RequireAndVerify, not merely request: asking for a certificate and accepting whatever arrives looks
    # identical in a log and provides nothing.
    ssl_client_certificate /certs/ca.crt;
    ssl_verify_client on;

    location / {
      # Echoed so a test can prove the certificate arrived rather than that a request succeeded.
      add_header X-Client-DN "$ssl_client_s_dn" always;
      add_header X-Client-Verify "$ssl_client_verify" always;
      return 200 "accepted\n";
    }
  }
}
EOF

echo "== container"
docker rm -f mtls-nginx >/dev/null 2>&1 || true
docker run -d --name mtls-nginx -p "$PORT:8443" \
  -v "$DIR:/certs:ro" \
  -v "$DIR/nginx.conf:/etc/nginx/nginx.conf:ro" \
  nginx:alpine >/dev/null

for _ in $(seq 1 30); do
  if curl -s --cacert "$DIR/ca.crt" --cert "$DIR/client.crt" --key "$DIR/client.key" \
    "https://127.0.0.1:$PORT/" -o /dev/null 2>/dev/null; then
    echo "listening on 127.0.0.1:$PORT"
    echo
    echo "run: go test ./internal/engine/ -run MutualTLS -v"
    exit 0
  fi
  sleep 0.5
done

echo "nginx did not become ready" >&2
docker logs mtls-nginx 2>&1 | tail -10 >&2
exit 1
