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

# Not distroless, and not for want of trying: distroless' bin and usr/bin are empty, and the
# ecosystem tools deps has to run are not self-contained binaries. `npm`, `npx` and
# `corepack` are scripts with `#!/usr/bin/env node`, `mvn`, `gradle` and `gradlew` start
# `#!/bin/sh`, `flutter` is bash, and `git` itself is absent; node also needs libstdc++,
# which distroless/base lacks. A distroless image would therefore lose npm, pnpm, yarn,
# Maven, Gradle, Flutter and postUpgradeTasks, which is most of what lock file regeneration
# is for. glibc also matters because the published Ruby, PHP and .NET builds are glibc.
#
# Instead of a smaller base, the runtime is pinned down by where it may write: the root file
# system can be mounted read-only (deploy/), and everything the daemon and its tools produce
# goes to the cache volume (mirrors, toolchains, their caches and $HOME) or to $TMPDIR,
# where post-processors mirror a scope before running a tool.
FROM debian:trixie-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git tar xz-utils \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 65532 --no-create-home --home-dir /var/lib/deps/home --shell /usr/sbin/nologin deps \
    && mkdir -p /var/lib/deps /nix && chown 65532:65532 /var/lib/deps /nix
COPY --from=build /out/deps /usr/local/bin/deps
# Repository code runs in the worker (gradle, bundler, setup.py), so the process is not root.
USER 65532:65532
# Both paths are created by the daemon at startup; nothing outside them is ever written.
ENV HOME=/var/lib/deps/home \
    TMPDIR=/tmp
# /nix is a mount of its own: a nix installation cannot be relocated (its binaries are linked
# against their own store paths), so a single-user nix needs a writable /nix. Without that
# mount the toolchain reports nix as not found, which is the behaviour of a machine without it.
VOLUME /var/lib/deps /nix
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/deps"]
CMD ["serve"]
