# Test image for Kubernetes steps and their module tests, on every cloud.
# Built from the repository root:
#   docker buildx build -f images/kubectl/Dockerfile -t entigolabs/entigo-infralib-test-kubectl:dev .
#
# It is the agent's Kubernetes image (entigolabs/entigo-infralib-kubectl:
# kubectl, argocd, helm, shackle, ei-agent and its entrypoint scripts, no
# OpenTofu and no cloud CLI) plus a Go toolchain, the infralib-test command and
# warmed Go caches for the framework packages of every cloud. When all the
# agent has to run is Kubernetes (ArgoCD) steps, the host orchestrator runs
# the agent and the module tests of every cloud from this one image; a run
# with OpenTofu steps uses the cloud's own test image for both.
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

FROM entigolabs/entigo-infralib-kubectl:${BASE_TAG}
ARG TARGETARCH
ARG GO_VERSION
USER root
# Module tests compile at run time inside this image. GOTOOLCHAIN=local keeps a
# newer go directive from downloading another toolchain; bump GO_VERSION instead.
# GOFLAGS=-mod=mod lets a test add a dependency the image did not cache: it is
# downloaded on the spot and the cache catches up on the next image build.
# -dwarf=false: nothing debugs a test binary, and the compiled packages in the
# build cache are a quarter smaller without the debug information.
# LOCAL_MODE: the agent's entrypoint scripts pass the plan to the apply stage
# through the filesystem (ei-agent --pipeline-type=local), not a CI artifact.
ENV LOCAL_MODE=true
ENV GOPATH=/go GOMODCACHE=/go/pkg/mod GOCACHE=/go/cache GOTOOLCHAIN=local GOFLAGS="-mod=mod -gcflags=all=-dwarf=false" \
    PATH=/usr/local/go/bin:/go/bin:$PATH \
    INFRALIB_TEST_CLOUD=kubectl
RUN wget --no-verbose -O go.tgz "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" \
    && tar -C /usr/local -xzf go.tgz && rm go.tgz \
    && rm -rf /usr/local/go/test /usr/local/go/doc /usr/local/go/misc
COPY --from=build /out/infralib-test /usr/local/bin/infralib-test
# Only the Go sources feed the cache warming, so a change to scripts or docs
# does not recompile everything.
COPY go.mod go.sum /opt/infralib-test/src/
COPY aws/ /opt/infralib-test/src/aws/
COPY google/ /opt/infralib-test/src/google/
COPY oracle/ /opt/infralib-test/src/oracle/
COPY azure/ /opt/infralib-test/src/azure/
COPY k8s/ /opt/infralib-test/src/k8s/
COPY env/ /opt/infralib-test/src/env/
COPY tf/ /opt/infralib-test/src/tf/
COPY logger/ /opt/infralib-test/src/logger/
COPY random/ /opt/infralib-test/src/random/
COPY retry/ /opt/infralib-test/src/retry/
COPY cmd/ /opt/infralib-test/src/cmd/
# Warm the module and build caches with what a module test of any cloud
# resolves: the framework packages of every cloud, the Kubernetes client and
# testify. A test that imports several clouds' packages finds them all
# compiled.
RUN cd /opt/infralib-test/src \
    && go build ./env/... ./logger/... ./random/... ./retry/... ./tf/... ./k8s/... ./aws/... ./google/... ./azure/... ./oracle/... \
    && go test -c -o /dev/null ./env/... \
    && go list -test -deps ./k8s/... ./aws/... ./google/... ./azure/... ./oracle/... >/dev/null \
    && chmod -R a+rwX /go
# Then shrink what was warmed: tests and fixtures, module zips, and the
# packages of large modules nobody imports (restored on demand by the run
# command, see images/prune-go-caches.sh).
COPY images/prune-go-caches.sh /opt/infralib-test/prune-go-caches.sh
RUN cd /opt/infralib-test/src && /opt/infralib-test/prune-go-caches.sh ./env/... ./logger/... ./random/... ./retry/... ./tf/... ./k8s/... ./aws/... ./google/... ./azure/... ./oracle/... \
    && chmod -R a+rwX /go
# The orchestrator runs this image as the executor's uid, which no passwd
# entry knows, so the real sudo refuses to run. The agent's scripts use sudo
# for one thing, update-ca-certificates after copying an environment's extra
# CA certificates (environments/ca-certificates) into the store. The store
# is made writable here and sudo is shimmed to run its command as is, so the
# certificates get installed whoever runs the container.
RUN chmod -R a+rwX /usr/local/share/ca-certificates && chmod a+rwX /etc/ssl/certs && chmod a+rw /etc/ssl/certs/ca-certificates.crt \
    && printf '#!/bin/sh\n# Shim: the test image runs as any uid, see the Dockerfile.\n[ "$1" = -n ] && shift\nexec "$@"\n' > /usr/local/bin/sudo \
    && chmod 755 /usr/local/bin/sudo
COPY scripts/ /opt/infralib-test/scripts/
COPY templates/ /opt/infralib-test/templates/
USER 1000
WORKDIR /app
