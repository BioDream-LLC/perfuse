#!/bin/sh
# setup.sh <version> - run on the test instance as root (provision.sh copies this directory there and runs it).
# Installs the release binary after checking its checksum, a Let's Encrypt certificate for test.perfuse.health, and the
# service. Safe to run again: it upgrades the binary and keeps the database.
set -eu
version=${1:?usage: setup.sh vX.Y.Z}
host=test.perfuse.health
here=$(cd "$(dirname "$0")" && pwd)

apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq curl certbot sqlite3 >/dev/null
id perfuse >/dev/null 2>&1 || useradd --system --home /opt/perfuse --shell /usr/sbin/nologin perfuse
install -d -o perfuse -g perfuse -m 0700 /opt/perfuse/data /opt/perfuse/channels /opt/perfuse/logs /opt/perfuse/backups
install -d -o root -g perfuse -m 0750 /opt/perfuse/conf /opt/perfuse/tls

# The release binary, checked against the published SHA256SUMS.
cd /tmp
base="https://github.com/BioDream-LLC/perfuse/releases/download/$version"
curl -fsSL -o perfuse.tgz "$base/perfuse-$version-linux-amd64.tar.gz"
curl -fsSL -o SHA256SUMS "$base/SHA256SUMS"
echo "$(grep " perfuse-$version-linux-amd64.tar.gz\$" SHA256SUMS | cut -d' ' -f1)  perfuse.tgz" | sha256sum -c -
tar xzf perfuse.tgz
install -m 0755 "perfuse-$version-linux-amd64/perfuse-linux-amd64" /opt/perfuse/perfuse
rm -rf perfuse.tgz SHA256SUMS "perfuse-$version-linux-amd64"

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

install -m 0644 "$here/perfuse-test.service" "$here/perfuse-backup.service" "$here/perfuse-backup.timer" /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now perfuse-backup.timer
systemctl enable --now perfuse-test
systemctl restart perfuse-test
sleep 3
curl -fsS "https://$host/livez" && echo " - Perfuse $version is up on https://$host"
