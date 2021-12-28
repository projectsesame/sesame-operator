#! /usr/bin/env bash

readonly HERE=$(cd "$(dirname "$0")" && pwd)
readonly REPO=$(cd "${HERE}/.." && pwd)
readonly PROGNAME=$(basename "$0")
readonly VERSION="$1"

if [ -z "$VERSION" ]; then
    printf "Usage: %s VERSION\n" "$PROGNAME"
    exit 1
fi

set -o errexit
set -o nounset
set -o pipefail

cd "${REPO}"

# Check that curl is installed.
if ! [ "$(which curl)" ] ; then
    echo "### You must have curl installed and set in PATH before running this script."
    exit 1
fi

# Verify connectivity to the Sesame CRD manifest URL.
URL="https://raw.githubusercontent.com/projectsesame/Sesame/${VERSION}/examples/Sesame/01-crds.yaml"
resp=$(curl -s -w %{http_code} -o /dev/null ${URL})
if [ "$resp" = "200" ] ; then
  echo "Generating the Sesame CRD YAML document..."
  curl -o config/crd/sesame/01-crds.yaml ${URL}
else
  echo "Failed to get the operator YAML document from ${URL}."
  exit 1
fi
