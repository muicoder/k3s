ARG GOLANG=golang:1.21.8-alpine3.18
FROM ${GOLANG} AS build


# Install necessary packages
RUN apk -U --no-cache add \
    bash git gcc musl-dev docker vim less file curl wget ca-certificates jq linux-headers \
    zlib-dev tar zip squashfs-tools npm coreutils python3 py3-pip openssl-dev libffi-dev libseccomp \
    libseccomp-dev libseccomp-static make libuv-static sqlite-dev sqlite-static libselinux \
    libselinux-dev zlib-dev zlib-static zstd pigz alpine-sdk binutils-gold btrfs-progs-dev \
    btrfs-progs-static gawk yq

# Install AWS CLI
RUN python3 -m pip install awscli

# Install Trivy
ENV TRIVY_VERSION="0.56.2"
RUN case "$(go env GOARCH)" in \
    arm64) TRIVY_ARCH="ARM64" ;; \
    amd64) TRIVY_ARCH="64bit" ;; \
    s390x) TRIVY_ARCH="s390x" ;; \
    *) TRIVY_ARCH="" ;; \
    esac
RUN if [ -n "${TRIVY_ARCH}" ]; then \
        wget --no-verbose "https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}/trivy_${TRIVY_VERSION}_Linux-${TRIVY_ARCH}.tar.gz" \
        && tar -zxvf "trivy_${TRIVY_VERSION}_Linux-${TRIVY_ARCH}.tar.gz" \
        && mv trivy /usr/local/bin; \
    fi

# Install goimports
RUN GOPROXY=direct go install golang.org/x/tools/cmd/goimports@gopls/v0.11.0

# Cleanup
RUN rm -rf /go/src /go/pkg


# Set SELINUX environment variable
ARG SELINUX=true
ENV STATIC_BUILD=true DOCKER_BUILDKIT=1 SKIP_IMAGE=1 SKIP_AIRGAP=1

WORKDIR /go/src/github.com/k3s-io/k3s
COPY . .

RUN bash scripts/ci;find bin dist -type f -print0 | xargs -0 file;cp -av dist/artifacts/k3s* /tmp/k3s-$(go env GOARCH)
FROM busybox:1.36.1
COPY --from=build /tmp/k3s-* /
