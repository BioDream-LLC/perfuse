#!/bin/sh
# strongswan-up.sh - two strongSwan gateways in Docker with a tunnel between them, for the strongSwan VPN test:
#   go test ./internal/vpn -run StrongSwanForReal   (with PERFUSE_STRONGSWAN set to what this prints)
#
# Gateway "ours" (172.30.0.2, 10.1.0.0/16) and "theirs" (172.30.0.3, 10.2.0.0/16) on a private Docker network, IKEv2 with a
# throwaway pre-shared key, AES-256/SHA-256/MODP-2048 and AES-256-GCM. Perfuse reads "ours" with swanctl, through a wrapper
# that runs it in the container. Stop with strongswan-down.sh.
set -eu
image=perfuse-strongswan-test
here=$(cd "$(dirname "$0")" && pwd)
work=${TMPDIR:-/tmp}/perfuse-strongswan
mkdir -p "$work"

docker build -q -t "$image" - >/dev/null <<'EOF'
FROM alpine:3.20
RUN apk add --no-cache strongswan
CMD ["/usr/lib/strongswan/charon"]
EOF

docker network inspect perfuse-vpn-test >/dev/null 2>&1 || docker network create --subnet 172.30.0.0/24 perfuse-vpn-test >/dev/null

conf() { # name local-ip remote-ip local-net remote-net
  cat <<EOF
connections {
  gw-gw {
    version = 2
    local_addrs = $2
    remote_addrs = $3
    proposals = aes256-sha256-modp2048
    local {
      auth = psk
      id = $2
    }
    remote {
      auth = psk
      id = $3
    }
    children {
      net-net {
        local_ts = $4
        remote_ts = $5
        esp_proposals = aes256gcm16
        start_action = start
      }
    }
  }
}
secrets {
  ike-1 {
    id-1 = 172.30.0.2
    id-2 = 172.30.0.3
    secret = "perfuse-test-only-not-a-secret"
  }
}
EOF
}

start() { # name ip local-net remote-ip remote-net
  docker rm -f "perfuse-swan-$1" >/dev/null 2>&1 || true
  conf "$1" "$2" "$4" "$3" "$5" > "$work/$1.conf"
  # Copied in rather than mounted: Docker on a Mac shares only some host directories, and the temporary one may not be among them.
  docker create --name "perfuse-swan-$1" --network perfuse-vpn-test --ip "$2" --cap-add NET_ADMIN --cap-add SYS_MODULE "$image" \
    sh -c '/usr/lib/strongswan/charon >/dev/null 2>&1 & sleep 2; swanctl --load-all >/dev/null; wait' >/dev/null
  docker cp -q "$work/$1.conf" "perfuse-swan-$1:/etc/swanctl/swanctl.conf"
  docker start "perfuse-swan-$1" >/dev/null
}

start theirs 172.30.0.3 10.2.0.0/16 172.30.0.2 10.1.0.0/16
start ours 172.30.0.2 10.1.0.0/16 172.30.0.3 10.2.0.0/16

# Wait for the child SA.
i=0
until docker exec perfuse-swan-ours swanctl --list-sas --ike gw-gw 2>/dev/null | grep -q "net-net: #.*INSTALLED"; do
  i=$((i + 1)); [ $i -lt 30 ] || { echo "strongswan-up.sh: the tunnel did not come up" >&2; docker logs perfuse-swan-ours 2>&1 | tail -20 >&2; exit 1; }
  sleep 1
done

cat > "$work/swanctl" <<'EOF'
#!/bin/sh
exec docker exec perfuse-swan-ours swanctl "$@"
EOF
chmod +x "$work/swanctl"
echo "$work/swanctl"
