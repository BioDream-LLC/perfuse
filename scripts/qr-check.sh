#!/usr/bin/env bash
# Decode Perfuse's QR codes with zbar, an independent reader, over a range of lengths and both error correction levels.
#
# The encoder in internal/shl is written here, so its own tests can only check structure. This is the check that it produces codes
# something else can read. Needs Docker; writes into ~/.cache/perfuse-qr, which Colima shares with containers.
set -euo pipefail
cd "$(dirname "$0")/.."
dir="$HOME/.cache/perfuse-qr"
rm -rf "$dir" && mkdir -p "$dir"
cat > internal/shl/zz_qrcheck_test.go <<'GO'
package shl

import (
	"fmt"
	"image/png"
	"os"
	"strings"
	"testing"
)

func TestZZWriteQRs(t *testing.T) {
	dir := os.Getenv("QRDIR")
	for _, n := range []int{1, 10, 17, 30, 60, 100, 150, 220, 300, 500, 800, 1200, 1800} {
		for _, e := range []ECC{ECCLow, ECCMedium} {
			text := fmt.Sprintf("n%d-", n) + strings.Repeat("shlink:/abcXYZ019-_", n/19+1)[:n]
			q, err := EncodeQR(text, e)
			if err != nil {
				t.Fatal(err)
			}
			img, _ := q.Image(4)
			f, _ := os.Create(fmt.Sprintf("%s/q-%d-%d-v%d.png", dir, n, e, q.Version))
			_ = png.Encode(f, img)
			_ = f.Close()
			_ = os.WriteFile(fmt.Sprintf("%s/q-%d-%d-v%d.txt", dir, n, e, q.Version), []byte(text), 0o644)
		}
	}
}
GO
trap 'rm -f internal/shl/zz_qrcheck_test.go' EXIT
QRDIR="$dir" go test ./internal/shl/ -run ZZWriteQRs >/dev/null
docker run --rm -v "$dir:/q" python:3.12-slim sh -c '
  apt-get update -qq >/dev/null 2>&1 && apt-get install -y -qq zbar-tools >/dev/null 2>&1
  cd /q; ok=0; bad=0
  for p in *.png; do
    if [ "$(zbarimg -q --raw "$p")" = "$(cat "${p%.png}.txt")" ]; then ok=$((ok+1)); else bad=$((bad+1)); echo "FAILED $p"; fi
  done
  echo "zbar decoded $ok, failed $bad"; [ "$bad" = 0 ]'
