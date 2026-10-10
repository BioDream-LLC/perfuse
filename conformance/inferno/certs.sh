#!/bin/sh
# certs.sh - a throwaway test CA and a certificate for Perfuse as Inferno's containers reach it (host.docker.internal), made
# once into .work/certs. Inferno checks TLS, so Perfuse is served over HTTPS with a certificate the containers trust (kit.sh).
set -eu
dir="$(cd "$(dirname "$0")" && pwd)/.work/certs"
[ -f "$dir/cert.pem" ] && exit 0
mkdir -p "$dir"
cd "$dir"
umask 077
openssl req -x509 -newkey rsa:2048 -nodes -days 825 -subj "/CN=Perfuse conformance test CA" \
  -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" \
  -keyout ca.key -out ca.pem 2>/dev/null
openssl req -newkey rsa:2048 -nodes -subj "/CN=host.docker.internal" -keyout key.pem -out srv.csr 2>/dev/null
printf 'subjectAltName=DNS:host.docker.internal,DNS:localhost,IP:127.0.0.1\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nbasicConstraints=CA:FALSE\n' > ext.cnf
openssl x509 -req -in srv.csr -CA ca.pem -CAkey ca.key -CAcreateserial -days 825 -extfile ext.cnf -out cert.pem 2>/dev/null
rm -f srv.csr ext.cnf ca.srl
chmod 644 ca.pem cert.pem
echo "test certificates written to $dir"
