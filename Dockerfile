# Cross-compile on the build host instead of emulating the target arch.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /vault-gitlab-operator ./cmd/vault-gitlab-operator

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /vault-gitlab-operator /usr/local/bin/vault-gitlab-operator
USER nonroot
ENTRYPOINT ["vault-gitlab-operator"]
CMD ["daemon", "--config", "/etc/vgo/config.yaml"]
