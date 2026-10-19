#!/bin/sh
# setup.sh <version|local> - run on the test instance as root (provision.sh copies this directory there and runs it).
# Installs the release binary after checking its checksum (or, with "local", the binary provision.sh built from this
# checkout and copied here), a Let's Encrypt certificate for test.perfuse.health, and the service; then fills the screens
# with invented data (seed.py). Safe to run again: it upgrades the binary and keeps the database.
set -eu
version=${1:?usage: setup.sh vX.Y.Z}
host=test.perfuse.health
here=$(cd "$(dirname "$0")" && pwd)

apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq curl certbot sqlite3 >/dev/null
id perfuse >/dev/null 2>&1 || useradd --system --home /opt/perfuse --shell /usr/sbin/nologin perfuse
install -d -o perfuse -g perfuse -m 0700 /opt/perfuse/data /opt/perfuse/channels /opt/perfuse/logs /opt/perfuse/backups /opt/perfuse/data/dicom
install -d -o root -g perfuse -m 0750 /opt/perfuse/conf /opt/perfuse/tls

if [ "$version" = local ]; then
  # Built by provision.sh from the checkout it ran in, with its checksum beside it.
  (cd "$here" && sha256sum -c perfuse-linux-amd64.sha256)
  install -m 0755 "$here/perfuse-linux-amd64" /opt/perfuse/perfuse
else
  # The release binary, checked against the published SHA256SUMS.
  cd /tmp
  base="https://github.com/BioDream-LLC/perfuse/releases/download/$version"
  curl -fsSL -o perfuse.tgz "$base/perfuse-$version-linux-amd64.tar.gz"
  curl -fsSL -o SHA256SUMS "$base/SHA256SUMS"
  echo "$(grep " perfuse-$version-linux-amd64.tar.gz\$" SHA256SUMS | cut -d' ' -f1)  perfuse.tgz" | sha256sum -c -
  tar xzf perfuse.tgz
  install -m 0755 "perfuse-$version-linux-amd64/perfuse-linux-amd64" /opt/perfuse/perfuse
  rm -rf perfuse.tgz SHA256SUMS "perfuse-$version-linux-amd64"
fi

# The demonstration channels. Replaced on every run, so the instance always shows what is in the repository; anything else
# in the folder (a channel somebody added by hand) is left alone.
install -o perfuse -g perfuse -m 0640 "$here"/channels/*.yaml /opt/perfuse/channels/

# The demonstration's own certificate authority for the TLS hop between adt-to-registry and patient-registry, the Fleet
# page's other (switched-off) site, and the sample drug formulary.
sudo -u perfuse sh "$here/demo-tls.sh" /opt/perfuse/data/demo-tls
[ -f /opt/perfuse/data/peers.yaml ] || install -o perfuse -g perfuse -m 0600 "$here/peers.yaml" /opt/perfuse/data/peers.yaml
install -o root -g perfuse -m 0640 "$here/formulary.yaml" /opt/perfuse/conf/
install -o root -g perfuse -m 0640 "$here/rules.yaml" "$here/clients.yaml" "$here/users.yaml" "$here/cds-clients.yaml" /opt/perfuse/conf/

# Let's Encrypt on port 80 (Perfuse does not use it); the renewal hook copies the certificate and restarts the service.
if [ ! -d "/etc/letsencrypt/live/$host" ]; then
  certbot certonly --standalone --non-interactive --agree-tos --register-unsafely-without-email -d "$host"
fi
cat > /etc/letsencrypt/renewal-hooks/deploy/perfuse.sh <<HOOK
#!/bin/sh
install -o root -g perfuse -m 0640 /etc/letsencrypt/live/$host/fullchain.pem /etc/letsencrypt/live/$host/privkey.pem /opt/perfuse/tls/
systemctl restart perfuse-test
HOOK
chmod 0755 /etc/letsencrypt/renewal-hooks/deploy/perfuse.sh
install -o root -g perfuse -m 0640 "/etc/letsencrypt/live/$host/fullchain.pem" "/etc/letsencrypt/live/$host/privkey.pem" /opt/perfuse/tls/

install -m 0644 "$here/perfuse-test.service" "$here/perfuse-backup.service" "$here/perfuse-backup.timer" \
  "$here/perfuse-traffic.service" "$here/perfuse-traffic.timer" "$here/perfuse-billing.service" /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now perfuse-billing
systemctl enable --now perfuse-backup.timer
systemctl enable --now perfuse-traffic.timer
systemctl enable --now perfuse-test
systemctl restart perfuse-test
sleep 3
curl -fsS "https://$host/livez" >/dev/null

token() { sudo -u perfuse /opt/perfuse/perfuse token create -db /opt/perfuse/data/perfuse.db -label "$1" -role "$2" 2>&1 |
  awk 'f && NF {print; exit} /token created/ {f=1}'; }

# The token clinical-documents publishes to this server's own document registry with, made once.
if ! grep -qs '^PERFUSE_DEMO_TOKEN=.' /opt/perfuse/conf/demo.env; then
  printf 'PERFUSE_DEMO_TOKEN=%s\n' "$(token document-publisher editor)" > /opt/perfuse/conf/demo.env
  chown root:perfuse /opt/perfuse/conf/demo.env; chmod 0640 /opt/perfuse/conf/demo.env
  systemctl restart perfuse-test; sleep 3
fi

# Data for every screen (invented), through the API with two administrators' tokens revoked straight after.
seed=$(token "seed-$(date +%s)" admin); approver=$(token "seed-approver-$(date +%s)" admin)
sudo -u perfuse env PERFUSE_SEED_TOKEN="$seed" PERFUSE_SEED_APPROVER_TOKEN="$approver" python3 "$here/seed.py" "https://$host" || true
# Whatever waited for the billing system while it was down (or given up on before it existed) goes now.
curl -fsS -m 10 -o /dev/null -H "Authorization: Bearer $seed" -H 'Content-Type: application/json' \
  -d '{"channel":"orders","destination":"billing-system"}' "https://$host/api/queue/retry" || true
for label in $(sudo -u perfuse /opt/perfuse/perfuse token list -db /opt/perfuse/data/perfuse.db | awk '$1 ~ /^seed-/ && $NF == "active" {print $1}'); do
  sudo -u perfuse /opt/perfuse/perfuse token revoke -db /opt/perfuse/data/perfuse.db -label "$label" >/dev/null
done
# TEFCA and the patient index settings take effect at a restart.
systemctl restart perfuse-test
sleep 3
curl -fsS "https://$host/livez" && echo " - Perfuse $version is up on https://$host"
