# Copyright 2026 The Kubernetes Authors.
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

IMAGE_REVIEWERS ?= @rikatz @youngnick @robscott @snorwin

# We need all the Make variables exported as env vars.
# Note that the ?= operator works regardless.

# Enable Go modules.
export GO111MODULE=on

# The registry to push container images to.
export REGISTRY ?= us-central1-docker.pkg.dev/k8s-staging-images/gateway-api

# These are overridden by cloudbuild.yaml when run by Prow.

# Prow gives this a value of the form vYYYYMMDD-hash.
# (It's similar to `git describe` output, and for non-tag
# builds will give vYYYYMMDD-COMMITS-HASH where COMMITS is the
# number of commits since the last tag.)
export GIT_TAG ?= dev

# Prow gives this the reference it's called on.
# The test-infra config job only allows our cloudbuild to
# be called on `main` and semver tags, so this will be
# set to one of those things.
export BASE_REF ?= main

# The commit hash of the current checkout
# Used to pass a binary version for main,
# overridden to semver for tagged versions.
# Cloudbuild will set this in the environment to the
# commit SHA, since the Prow does not seem to check out
# a git repo.
export COMMIT ?= $(shell git rev-parse --short HEAD)

DOCKER ?= docker
# TOP is the current directory where this Makefile lives.
TOP := $(dir $(firstword $(MAKEFILE_LIST)))
# ROOT is the root of the documentation tree.
ROOT := $(abspath $(TOP))

# Run static analysis.
.PHONY: verify
verify:
	hack/verify-all.sh -v

# Run generators for protos, Deepcopy funcs, CRDs, and docs.
.PHONY: generate
generate:
	hack/update-protos.sh

# Run go test against code
test:
	go test -race -cover ./...

# Verify if support Docker Buildx.
.PHONY: image.buildx.verify
image.buildx.verify:
	docker version
	$(eval PASS := $(shell docker buildx --help | grep "docker buildx" ))
	@if [ -z "$(PASS)" ]; then \
		echo "Cannot find docker buildx, please install first."; \
		exit 1;\
	else \
		echo "===========> Support docker buildx"; \
		docker buildx version; \
	fi

export BUILDX_CONTEXT = gateway-api-builder
export BUILDX_PLATFORMS = linux/amd64,linux/arm64

# Setup multi-arch docker buildx environment.
.PHONY: image.multiarch.setup
image.multiarch.setup: image.buildx.verify
# Ensure qemu is in binfmt_misc.
# Docker desktop already has these in versions recent enough to have buildx,
# We only need to do this setup on linux hosts.
	@if [ "$(shell uname)" == "Linux" ]; then \
		docker run --rm --privileged multiarch/qemu-user-static --reset -p yes; \
	fi
# Reuse existing builder if available, otherwise create one.
	@if ! docker buildx inspect $(BUILDX_CONTEXT) >/dev/null 2>&1; then \
		docker buildx create --use --name $(BUILDX_CONTEXT) --platform "$(BUILDX_PLATFORMS)"; \
	else \
		docker buildx use $(BUILDX_CONTEXT); \
	fi

# Build and Push Multi Arch Images.
.PHONY: release-staging
release-staging: image.multiarch.setup
	hack/build-and-push.sh


#### PROMOTION TARGETS
KPROMO_VER := v4.5.1
# KPROMO_PKG may have to be changed if KPROMO_VER increases its major version.
KPROMO_PKG := sigs.k8s.io/promo-tools/v4/cmd/kpromo
USER_FORK ?= $(shell git config --get remote.origin.url | cut -d: -f2 | cut -d/ -f1)
TOOLS_DIR := hack/tools
TOOLS_BIN_DIR := $(abspath $(TOOLS_DIR))/bin
KPROMO := $(TOOLS_BIN_DIR)/kpromo

.PHONY: promote-images
promote-images: $(KPROMO)
ifndef RELEASE_TAG
	$(error RELEASE_TAG is not set. Usage: export RELEASE_TAG=v0.0.1 && make promote-images)
endif
	$(TOOLS_BIN_DIR)/kpromo pr --project gateway-api-conformance-images --tag $(RELEASE_TAG) --reviewers "$(IMAGE_REVIEWERS)" --fork $(USER_FORK) --image echo-basic --image echo-advanced

$(KPROMO):
	mkdir -p $(TOOLS_BIN_DIR)
	GOBIN=$(TOOLS_BIN_DIR) go install $(KPROMO_PKG)@$(KPROMO_VER)