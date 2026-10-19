#!/bin/sh
# provision.sh <version|main> - creates or updates the public test instance, test.perfuse.health, from this machine.
# "main" builds the Linux binary from this checkout instead of installing a release.
#
# One Amazon Lightsail instance (nano: 512 MB, 2 vCPUs, about $5 a month with its IPv4 address), a static IP, the DNS
# record in the perfuse.health zone, and setup.sh run on it. Ports 80 and 443 are open to everybody; SSH only to the
# address this is run from. Run again to upgrade to another release: the database and the certificate are kept.
#
# Needs the AWS CLI signed in to the account that holds the perfuse.health zone. The SSH key is kept in $KEYS (default
# ~/keys), mode 600, and never printed.
set -eu
version=${1:?usage: provision.sh vX.Y.Z|main}
here=$(cd "$(dirname "$0")" && pwd)
export AWS_PAGER=""
region=us-east-1
name=perfuse-test
host=test.perfuse.health
keys=${KEYS:-$HOME/keys}
key="$keys/lightsail-$region.pem"

if ! aws lightsail get-instance --region $region --instance-name $name >/dev/null 2>&1; then
  echo "creating $name"
  aws lightsail create-instances --region $region --instance-names $name --availability-zone ${region}a \
    --blueprint-id debian_12 --bundle-id nano_3_0 --tags key=purpose,value=perfuse-public-test >/dev/null
fi
if [ ! -s "$key" ]; then
  umask 077
  aws lightsail download-default-key-pair --region $region --query privateKeyBase64 --output text > "$key"
fi

until [ "$(aws lightsail get-instance-state --region $region --instance-name $name --query state.name --output text)" = running ]; do
  sleep 5
done

if ! aws lightsail get-static-ip --region $region --static-ip-name $name-ip >/dev/null 2>&1; then
  aws lightsail allocate-static-ip --region $region --static-ip-name $name-ip >/dev/null
fi
attached=$(aws lightsail get-static-ip --region $region --static-ip-name $name-ip --query staticIp.attachedTo --output text)
[ "$attached" = "$name" ] || aws lightsail attach-static-ip --region $region --static-ip-name $name-ip --instance-name $name >/dev/null
ip=$(aws lightsail get-static-ip --region $region --static-ip-name $name-ip --query staticIp.ipAddress --output text)

mine=$(curl -fsS https://checkip.amazonaws.com)
# 80, 443, and SSH from here. Opened one by one rather than replacing the list, which closed the Vaglio demo's port on the
# same instance every time this ran.
for p in "fromPort=80,toPort=80,protocol=tcp" "fromPort=443,toPort=443,protocol=tcp" "fromPort=22,toPort=22,protocol=tcp,cidrs=$mine/32"; do
  aws lightsail open-instance-public-ports --region $region --instance-name $name --port-info "$p" >/dev/null
done

zone=$(aws route53 list-hosted-zones-by-name --dns-name perfuse.health --query 'HostedZones[0].Id' --output text)
aws route53 change-resource-record-sets --hosted-zone-id "$zone" --change-batch "{\"Changes\":[{\"Action\":\"UPSERT\",
  \"ResourceRecordSet\":{\"Name\":\"$host\",\"Type\":\"A\",\"TTL\":300,\"ResourceRecords\":[{\"Value\":\"$ip\"}]}}]}" >/dev/null
until [ "$(dig +short $host @8.8.8.8 | tail -1)" = "$ip" ]; do sleep 10; done
echo "$host is $ip"

ssh_opts="-i $key -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=$keys/known_hosts -o ConnectTimeout=10"
until ssh $ssh_opts admin@"$ip" true 2>/dev/null; do sleep 5; done
ssh $ssh_opts admin@"$ip" 'rm -rf /tmp/perfuse-test && mkdir -p /tmp/perfuse-test/channels'
scp -q $ssh_opts "$here"/setup.sh "$here"/demo-tls.sh "$here"/seed.py "$here"/perfuse-*.service "$here"/perfuse-*.timer \
  "$here"/*.yaml "$here"/../../examples/formulary/formulary.yaml admin@"$ip":/tmp/perfuse-test/
if [ "$version" = main ]; then
  out=$(mktemp -d)
  (cd "$here/../.." && make web >/dev/null && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
    -ldflags "-s -w -X main.version=$(git describe --tags --always)-main" -o "$out/perfuse-linux-amd64" ./cmd/perfuse)
  (cd "$out" && shasum -a 256 perfuse-linux-amd64 > perfuse-linux-amd64.sha256)
  scp -q $ssh_opts "$out"/perfuse-linux-amd64 "$out"/perfuse-linux-amd64.sha256 admin@"$ip":/tmp/perfuse-test/
  rm -rf "$out"
  version=local
fi
scp -q $ssh_opts "$here"/channels/*.yaml admin@"$ip":/tmp/perfuse-test/channels/
ssh $ssh_opts admin@"$ip" "sudo sh /tmp/perfuse-test/setup.sh $version"
