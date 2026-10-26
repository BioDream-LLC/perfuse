#!/bin/sh
# kit.sh <crd|dtr|pas|smart|uscore> - checks out the Inferno test kit at the version Perfuse was verified with, starts it in Docker,
# and makes its containers trust the test CA that signs Perfuse's certificate (certs.sh). Only one kit runs at a time: they
# all answer on http://localhost.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
work="$here/.work"
name=${1:?usage: kit.sh crd|dtr|pas|smart|uscore|pacio-toc|pacio-smp|pacio}

owner=inferno-framework
case "$name" in
  crd)    repo=davinci-crd-test-kit tag=v0.14.2 ;;
  dtr)    repo=davinci-dtr-test-kit tag=v0.18.0 ;;
  pas)    repo=davinci-pas-test-kit tag=v0.15.2 ;;
  smart)  repo=smart-app-launch-test-kit tag=v1.0.3 ;;
  uscore) repo=us-core-test-kit    tag=v1.1.6 ;;
  # PACIO's kits have no release tags: pinned by commit.
  pacio-toc) owner=paciowg repo=pacio-toc-test-kit commit=6141da0acaca09c9e320a2b9957c7686f891a125 ;;
  pacio-smp) owner=paciowg repo=pacio-smp-test-kit commit=03892d5a74c5655c31ed79429ac1eb9f5207c3ea ;;
  pacio)     owner=paciowg repo=pacio-test-kit     commit=8bac2a758f7642563124cf9d8790a9810767f85f ;;
  *) echo "kit.sh: no kit called $name" >&2; exit 2 ;;
esac

"$here/certs.sh"

kit="$work/kits/$name"
if [ -n "${commit:-}" ]; then
  if [ ! -d "$kit/.git" ]; then
    mkdir -p "$kit"
    git -C "$kit" init -q
    git -C "$kit" fetch -q --depth 1 "https://github.com/$owner/$repo.git" "$commit"
    git -C "$kit" -c advice.detachedHead=false checkout -q FETCH_HEAD
  fi
  [ "$(git -C "$kit" rev-parse HEAD)" = "$commit" ] || { echo "kit.sh: $kit is not at $commit" >&2; exit 1; }
  tag=$commit
else
  if [ ! -d "$kit/.git" ]; then
    mkdir -p "$work/kits"
    git -c advice.detachedHead=false clone -q --depth 1 --branch "$tag" "https://github.com/$owner/$repo.git" "$kit"
  fi
  [ "$(git -C "$kit" describe --tags --exact-match 2>/dev/null)" = "$tag" ] || { echo "kit.sh: $kit is not at $tag" >&2; exit 1; }
fi

# SUBSCRIPTIONS_KIT_REF runs the PAS kit with the Subscriptions test kit (which supplies its notification checks) at a commit of
# its own repository, to try an upstream fix before it is released. The kit's Gemfile.lock is kept, so bundler moves only what
# that gem needs; the checkout is put back first, so a run without it is the kit as released.
git -C "$kit" checkout -q -- Gemfile Gemfile.lock 2>/dev/null || true
if [ -n "${SUBSCRIPTIONS_KIT_REF:-}" ] && [ "$name" = pas ]; then
  printf '\ngem "subscriptions_test_kit", git: "https://github.com/inferno-framework/subscriptions-test-kit.git", ref: "%s"\n' \
    "$SUBSCRIPTIONS_KIT_REF" >> "$kit/Gemfile"
  # Re-lock that gem alone, in the kit's own Ruby image, so every other gem stays at the version the kit was released with.
  ruby_image=$(sed -n 's/^FROM //p' "$kit/Dockerfile" | head -1)
  docker run --rm -v "$kit:/w" -w /w "$ruby_image" sh -c \
    'gem install bundler -q >/dev/null 2>&1; bundle lock --update subscriptions_test_kit >/dev/null' ||
    { echo "kit.sh: could not lock the Subscriptions test kit at $SUBSCRIPTIONS_KIT_REF" >&2; exit 1; }
  echo "kit.sh: the Subscriptions test kit at $SUBSCRIPTIONS_KIT_REF (not a release)"
fi

# The validator needs more memory than the kits give it for Perfuse's larger bundles, and keeps its package cache between runs.
# CRD's simulated EHR hands out its own address to Perfuse, which reaches it from this machine's side of Docker.
{
  echo "services:"
  echo "  hl7_validator_service:"
  echo "    environment:"
  echo "      SESSION_CACHE_DURATION: 30"
  echo "      JAVA_TOOL_OPTIONS: \"-Xmx4g -XX:+UseG1GC\""
  # The general PACIO kit's .env.production points FHIRPath at localhost, which inside its containers is nothing; the
  # evaluator is its own service.
  if [ "$name" = pacio ]; then
    for s in inferno worker; do
      echo "  $s:"
      echo "    environment:"
      echo "      FHIRPATH_URL: \"http://fhirpath:6789\""
    done
  fi
  if [ "$name" = crd ]; then
    for s in inferno worker; do
      echo "  $s:"
      echo "    environment:"
      echo "      INFERNO_HOST: \"http://host.docker.internal\""
    done
  fi
} > "$kit/docker-compose.override.yml"

# Stop any other kit first.
for other in "$work"/kits/*; do
  [ "$other" = "$kit" ] || [ ! -d "$other" ] || (cd "$other" && docker compose down >/dev/null 2>&1 || true)
done

cd "$kit"
docker compose pull -q >/dev/null 2>&1 || true
docker compose build -q >/dev/null
# The kit's own setup.sh step: create or upgrade Inferno's database before it starts.
docker compose run --rm -T inferno bundle exec inferno migrate >/dev/null 2>&1
docker compose up -d >/dev/null
printf "waiting for Inferno"
i=0
until curl -sf http://localhost/api/test_suites >/dev/null 2>&1; do
  i=$((i + 1)); [ $i -lt 120 ] || { echo; echo "kit.sh: Inferno did not answer on http://localhost" >&2; exit 1; }
  # nginx gives up for good if it starts before the inferno container has a name to resolve; start it again.
  if [ $((i % 6)) -eq 0 ] && [ -z "$(docker compose ps -q --status running nginx 2>/dev/null)" ]; then docker compose up -d nginx >/dev/null 2>&1 || true; fi
  printf "."; sleep 5
done
echo

# nginx resolves the inferno container's address once, so a kit whose inferno container was recreated (new settings) answers
# 502 until nginx starts again.
docker compose restart nginx >/dev/null 2>&1 || true
sleep 2

# Trust the test CA inside the containers that call Perfuse, then restart them: Ruby reads the CA bundle once, when it starts.
# Appended once; a restart keeps it, a recreated container starts afresh and gets it again here.
for s in inferno worker; do
  docker compose exec -T -u root "$s" sh -c 'grep -q "Perfuse conformance test CA" /etc/ssl/certs/ca-certificates.crt ||
    { echo "# Perfuse conformance test CA"; cat; } >> /etc/ssl/certs/ca-certificates.crt' < "$work/certs/ca.pem"
done
docker compose restart inferno worker >/dev/null 2>&1
i=0
until curl -sf http://localhost/api/test_suites >/dev/null 2>&1; do
  i=$((i + 1)); [ $i -lt 60 ] || { echo "kit.sh: Inferno did not come back after the restart" >&2; exit 1; }
  sleep 3
done
echo "$repo $tag is up on http://localhost"
