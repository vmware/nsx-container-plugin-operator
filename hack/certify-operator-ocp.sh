#!/bin/bash -x
set -eo pipefail

function cleanup {
    $CONTAINER_TOOL image rm -f quay.io/opdev/preflight:stable
}

trap cleanup EXIT

CONTAINER_TOOL=${CONTAINER_TOOL:-docker}
CONTAINER_REGISTRY=${CONTAINER_REGISTRY:-quay.io}

$CONTAINER_TOOL run \
  --rm \
  --security-opt=label=disable \
  --env PFLT_LOGLEVEL=trace \
  --env PFLT_CERTIFICATION_PROJECT_ID=$PFLT_CERTIFICATION_PROJECT_ID \
  --env PFLT_PYXIS_API_TOKEN=$PFLT_PYXIS_API_TOKEN \
  quay.io/opdev/preflight:stable check container -s docker.io/vmware/nsx-container-plugin-operator:$VERSION

exit 0
