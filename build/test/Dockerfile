# The Linux test image (Plan 7d-2, Decision 12). make test-linux runs the suite in it, as
# the developer's own uid, on this Docker engine: build/test/run.sh holds the steps. It
# runs no linter; make lint lints for Linux on the host.
FROM golang:1.26@sha256:670bb5aa5349ae03b8516764718486f4f16399134bdc9acd0c46a23f043ab05c

# The suite shells out to docker, so the image takes the Docker CLI from Docker's static
# release, pinned by version and checksum, and installs docker alone. The build fails when
# a checksum does not match.
ARG TARGETARCH
RUN set -eu; \
	case "$TARGETARCH" in \
		amd64) arch=x86_64; sum=56c90317a05d86bd84f2bf8d086faefce6f31f82e8c92880de7710dd5f8ce5b7 ;; \
		arm64) arch=aarch64; sum=f3b260fd888b12b34dc01b92c1e4d2576b1167a7d7e60e923d5782798fbefeb7 ;; \
		*) echo "no Docker CLI for the architecture '$TARGETARCH'" >&2; exit 1 ;; \
	esac; \
	curl -fsSL -o /tmp/docker.tgz "https://download.docker.com/linux/static/stable/$arch/docker-29.4.0.tgz"; \
	echo "$sum  /tmp/docker.tgz" | sha256sum -c -; \
	tar -xzf /tmp/docker.tgz --no-same-owner -C /usr/local/bin --strip-components=1 docker/docker; \
	rm /tmp/docker.tgz; \
	docker --version

# jq, which the live gate's steps use.
RUN set -eu; \
	apt-get update; \
	DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends jq; \
	rm -rf /var/lib/apt/lists/*

# The module cache, the build cache and a home directory, writable by any user, so that a
# named volume mounted there takes that mode.
RUN mkdir -p -m 0777 /go/pkg/mod /cache/go-build /home/test
ENV GOCACHE=/cache/go-build HOME=/home/test GOTOOLCHAIN=local
