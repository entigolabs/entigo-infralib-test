# The small image a module repository's test.sh bootstraps from: the
# infralib-test command, the orchestrator scripts and the templates, nothing
# cloud specific. Built from the repository root:
#   docker buildx build -f images/cli/Dockerfile -t entigolabs/entigo-infralib-test-cli:dev .
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

FROM alpine:3.22
RUN apk --no-cache add bash
COPY --from=build /out/infralib-test /usr/local/bin/infralib-test
COPY scripts/ /opt/infralib-test/scripts/
COPY templates/ /opt/infralib-test/templates/
USER 1000
WORKDIR /conf
ENTRYPOINT ["infralib-test"]
