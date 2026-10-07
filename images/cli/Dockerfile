# The small image a module repository's test.sh bootstraps from: the
# infralib-test command, the orchestrator scripts and the templates, nothing
# cloud specific. Built from the repository root:
#   docker buildx build -f images/cli/Dockerfile -t entigolabs/entigo-infralib-test-cli:dev .
ARG GO_VERSION=1.26.8

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/infralib-test ./cmd/infralib-test

FROM alpine:3.22
RUN apk --no-cache add bash
COPY --from=build /out/infralib-test /usr/local/bin/infralib-test
COPY scripts/ /opt/infralib-test/scripts/
COPY templates/ /opt/infralib-test/templates/
USER 1000
WORKDIR /conf
ENTRYPOINT ["infralib-test"]
