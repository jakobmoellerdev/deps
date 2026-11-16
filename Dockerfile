# The image of `deps serve`. It ships git, CA certificates, tar and xz and the deps binary,
# and nothing else: the ecosystem toolchains a repository needs are downloaded at runtime
# into the cache volume, verified against the publisher's checksum (see `deps help serve`).
# A fat image with every ecosystem preinstalled would be gigabytes and still wrong for a
# repository that pins another version.
FROM golang:1.27 AS build
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -buildid= -X main.version=${VERSION}" -o /out/deps ./cmd/deps

# glibc rather than distroless: the published Ruby, PHP and .NET builds the toolchain
# installer fetches are glibc builds, and tar with xz support extracts several of them.
FROM debian:trixie-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git tar xz-utils \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 65532 --create-home --home-dir /home/deps --shell /usr/sbin/nologin deps \
    && mkdir -p /var/lib/deps && chown 65532:65532 /var/lib/deps
COPY --from=build /out/deps /usr/local/bin/deps
# Repository code runs in the worker (gradle, bundler, setup.py), so the process is not root.
USER 65532:65532
ENV HOME=/home/deps
VOLUME /var/lib/deps
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/deps"]
CMD ["serve"]
