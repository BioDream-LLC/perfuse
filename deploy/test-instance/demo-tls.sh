#!/bin/sh
# demo-tls.sh DIR - the demonstration's own certificate authority and the patient registry's certificate (invented
# names, valid for 398 days), so one MLLP hop runs over TLS and the Certificates screen has something to show. Kept if
# present and more than 30 days from expiry.
set -eu
dir=${1:?usage: demo-tls.sh DIR}
mkdir -p "$dir"
cd "$dir"
if [ -s registry.pem ] && openssl x509 -checkend 2592000 -noout -in registry.pem >/dev/null 2>&1; then
  exit 0
fi
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 398 -subj "/O=Riverside Health (demonstration)/CN=Riverside demonstration CA" \
  -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" -keyout ca.key -out ca.pem 2>/dev/null
openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj "/O=Riverside Health (demonstration)/CN=registry.riverside.example" \
  -keyout registry.key -out registry.csr 2>/dev/null
printf 'subjectAltName=DNS:registry.riverside.example\nextendedKeyUsage=serverAuth\nkeyUsage=critical,digitalSignature\n' > registry.ext
openssl x509 -req -in registry.csr -CA ca.pem -CAkey ca.key -CAcreateserial -days 397 -extfile registry.ext -out registry.pem 2>/dev/null
rm -f registry.csr registry.ext ca.srl
chmod 0640 ca.pem registry.pem registry.key
chmod 0600 ca.key
