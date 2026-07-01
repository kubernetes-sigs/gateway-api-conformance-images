#!/bin/bash

# Copyright 2023 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_ROOT=$(dirname "${BASH_SOURCE}")/..

BUILDX_CONTEXT="${BUILDX_CONTEXT:-gateway-api-builder}"
BUILDX_PLATFORMS="${BUILDX_PLATFORMS:-linux/amd64}"
REGISTRY="${REGISTRY:-us-central1-docker.pkg.dev/k8s-staging-images/gateway-api}"
GIT_TAG="${GIT_TAG:-dev}"
BASE_REF="${BASE_REF:-main}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD)}"
export BUILDX_CONTEXT BUILDX_PLATFORMS REGISTRY GIT_TAG BASE_REF COMMIT

echo "Verifying docker images"

docker buildx rm "${BUILDX_CONTEXT}" || true
docker buildx create --use --name "${BUILDX_CONTEXT}" --platform "${BUILDX_PLATFORMS}"

VERIFY=true "${SCRIPT_ROOT}/hack/build-and-push.sh"

