# Test image for google environments. Built from the repository root:
#   docker buildx build -f images/google/Dockerfile -t entigolabs/entigo-infralib-test-google:dev .
#
# It is the agent image of the cloud (entigolabs/entigo-infralib-google: tofu,
# kubectl, helm, the cloud CLI, ei-agent and its entrypoint scripts) plus a Go
# toolchain, the infralib-test command and warmed Go caches for the framework
# packages a google module test imports. The host orchestrator runs both the
# agent and the module tests from this one image, so the two can never disagree
# about tool versions.
ARG BASE_TAG=latest
ARG GO_VERSION=1.26.8
# The framework version this image is built from (tag, or commit sha); the
# run command aligns a repository's go.mod to it.
ARG VERSION=dev

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS TARGETARCH VERSION
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/infralib-test ./cmd/infralib-test

FROM entigolabs/entigo-infralib-google:${BASE_TAG}
ARG TARGETARCH
ARG GO_VERSION
USER root
# Module tests compile at run time inside this image. GOTOOLCHAIN=local keeps a
# newer go directive from downloading another toolchain; bump GO_VERSION instead.
# GOFLAGS=-mod=mod lets a test add a dependency the image did not cache: it is
# downloaded on the spot and the cache catches up on the next image build.
# LOCAL_MODE: the agent's entrypoint scripts pass the plan to the apply stage
# through the filesystem (ei-agent --pipeline-type=local), not a CI artifact.
ENV LOCAL_MODE=true
ENV GOPATH=/go GOMODCACHE=/go/pkg/mod GOCACHE=/go/cache GOTOOLCHAIN=local GOFLAGS=-mod=mod \
    PATH=/usr/local/go/bin:/go/bin:$PATH \
    INFRALIB_TEST_CLOUD=google
RUN wget --no-verbose -O go.tgz "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" \
    && tar -C /usr/local -xzf go.tgz && rm go.tgz \
    && rm -rf /usr/local/go/test /usr/local/go/doc /usr/local/go/misc
COPY --from=build /out/infralib-test /usr/local/bin/infralib-test
COPY scripts/ /opt/infralib-test/scripts/
COPY templates/ /opt/infralib-test/templates/
COPY . /opt/infralib-test/src/
# Warm the module and build caches with what a google module test resolves:
# the framework packages of this cloud, the Kubernetes client and testify.
RUN cd /opt/infralib-test/src \
    && go build ./env/... ./logger/... ./random/... ./retry/... ./tf/... ./k8s/... ./google/... \
    && go test -c -o /dev/null ./env/... \
    && go list -test -deps ./k8s/... ./google/... >/dev/null \
    && find /go/pkg/mod -type f -name examples-1.json -delete \
    && chmod -R a+rwX /go
USER 1000
WORKDIR /app
