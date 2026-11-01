#!/bin/bash
# The runtime image's build (Plan 7d-2, Decision 6, Contract C12), run from the checkout's
# root, as make image and make image-dev run it, with the Docker engine's buildx plugin.
#
# Usage: build/runtime/image.sh commit COMMIT
#        build/runtime/image.sh dev BINARY TAG CONTAINER
#        build/runtime/image.sh remove ID TAG
#
# commit builds the runtime image of COMMIT from the commit alone, so that one commit gives
# one image id on one engine, whatever the checkout or the developer's Go environment holds.
# The binary comes from git archive of the commit: static, its version the commit's id and
# its date the commit's time, built by the toolchain the commit's go.mod names, in a clean
# Go environment, for the engine's architecture or ARCH's. The image comes from the commit's
# own Dockerfile, without the cache or attestations, its times the commit's. It tags the
# image agentswarm-runtime:<its id's 64 hex digits>, leaves the build's own name,
# agentswarm-runtime:<the commit's id>, and prints the binary's SHA-256 and the image's id.
# A commit without build/runtime/Dockerfile is refused: its runtime predates the image
# contract.
#
# dev puts the checkout's Dockerfile around BINARY, the worktree's Linux binary, names the
# image TAG, the worktree's own, and prints its id; it promises no reproducibility. The
# image TAG named before, its own worktree's alone, is then removed after a backup, so that
# each gate does not leave the last one's dangling. While a container named CONTAINER
# exists, the worktree's test container, dev refuses and changes nothing: a test run may be
# using the image TAG names.
#
# remove removes the image ID, which the tag TAG named before, after a backup, as dev removes
# its predecessor (remove, below): make stubborn-image calls it for the stubborn test image's
# predecessor, once a build gives that image another id.
set -eu -o pipefail

module=github.com/jchen0426/agent-swarm
# The go command, which make passes as its GO.
go=${GO:-go}

usage() {
	echo "usage: build/runtime/image.sh commit COMMIT | dev BINARY TAG CONTAINER | remove ID TAG" >&2
	exit 2
}

# image_id prints the id of the image $1 names, as the engine gives it: sha256: and 64 hex
# digits, the id ImageInspect and ContainerCreate use on either image store.
image_id() {
	local id
	id=$(docker image inspect --format '{{.Id}}' "$1")
	if [[ ! $id =~ ^sha256:[0-9a-f]{64}$ ]]; then
		echo "build/runtime/image.sh: the engine gives $1 the id '$id', which is not sha256: and 64 hex digits" >&2
		return 1
	fi
	echo "$id"
}

# sha256 prints the SHA-256 of the file $1, or fails: bash runs it without set -e inside the
# caller's $(...), so each failure is returned here.
sha256() {
	local sum
	if command -v sha256sum >/dev/null; then
		sum=$(sha256sum "$1") || return
	else
		sum=$(shasum -a 256 "$1") || return
	fi
	sum=${sum%% *}
	if [[ ! $sum =~ ^[0-9a-f]{64}$ ]]; then
		echo "build/runtime/image.sh: the SHA-256 of $1 came out as '$sum', which is not 64 hex digits" >&2
		return 1
	fi
	echo "$sum"
}

# toolchain prints the toolchain the go.mod $1, of the commit $2, names: its toolchain line,
# or else the version of its go line, as go1.26.0.
toolchain() {
	local name
	name=$(awk '$1 == "toolchain" { t = $2 } $1 == "go" { g = "go" $2 } END { print (t != "" ? t : g) }' "$1")
	if [[ ! $name =~ ^go[0-9]+\.[0-9]+(\.[0-9]+)?((rc|beta)[0-9]+)?$ ]]; then
		echo "build/runtime/image.sh: the go.mod of $2 names no toolchain to build with, but '$name'" >&2
		return 1
	fi
	echo "$name"
}

# build_commit builds the runtime image of the commit $1 in the directory $work.
build_commit() {
	local commit epoch stamp date arch go_version sum id
	commit=$(git rev-parse --verify "$1^{commit}")
	if ! git cat-file -e "$commit:build/runtime/Dockerfile" 2>/dev/null; then
		echo "build/runtime/image.sh: $commit has no build/runtime/Dockerfile: its runtime predates the image contract" >&2
		return 1
	fi
	epoch=$(git log -1 --format=%ct "$commit")
	stamp=$(TZ=UTC git log -1 --date=format-local:%Y%m%d%H%M.%S --format=%cd "$commit")
	date=$(TZ=UTC git log -1 --date=format-local:%Y-%m-%dT%H:%M:%SZ --format=%cd "$commit")
	arch=${ARCH:-$(docker version --format '{{.Server.Arch}}')}

	mkdir "$work/src" "$work/context"
	git archive "$commit" | tar -x -C "$work/src"
	go_version=$(toolchain "$work/src/go.mod" "$commit")
	(
		cd "$work/src"
		# A clean Go environment, so that nothing of the developer's reaches the binary: no go
		# env -w setting, flags, workspace, experiment, FIPS 140 mode or architecture's level.
		unset GO386 GOAMD64 GOARM GOARM64 GOFIPS140 GOMIPS GOMIPS64 GOPPC64 GORISCV64 GOWASM
		export GOENV=off GOFLAGS='' GOWORK=off GOEXPERIMENT='' GOTOOLCHAIN="$go_version" CGO_ENABLED=0 GOOS=linux GOARCH="$arch"
		"$go" build -trimpath -buildvcs=false \
			-ldflags "-s -w -X $module/internal/versioninfo.version=$commit -X $module/internal/versioninfo.commit=$commit -X $module/internal/versioninfo.date=$date" \
			-o "$work/context/agentswarm" ./cmd/agentswarm
	)
	# The binary's mtime is the commit's time, in UTC, as the layer holds it.
	TZ=UTC touch -t "$stamp" "$work/context/agentswarm"
	sum=$(sha256 "$work/context/agentswarm")
	# The platform is the binary's, so that an ARCH other than the engine's is the image's too.
	SOURCE_DATE_EPOCH=$epoch docker buildx build --no-cache --provenance=false --sbom=false --platform "linux/$arch" \
		--build-arg VERSION="$commit" --build-arg GO="$go_version" \
		--output "type=image,name=agentswarm-runtime:$commit,rewrite-timestamp=true" \
		-f "$work/src/build/runtime/Dockerfile" "$work/context"

	id=$(image_id "agentswarm-runtime:$commit")
	docker tag "$id" "agentswarm-runtime:${id#sha256:}"
	echo "binary sha256:$sum"
	echo "image $id"
}

# build_dev builds the runtime image around the binary $1 in the directory $work, names it
# $2, and removes the image $2 named before. It refuses, changing nothing, while a container
# named $3 exists.
build_dev() {
	local binary=$1 tag=$2 container=$3 old version id
	if docker container inspect "$container" >/dev/null 2>&1; then
		echo "build/runtime/image.sh: a container named $container exists, so a test run of this worktree may be using the image $tag names; once no run is live, remove it with: docker rm -f $container" >&2
		return 1
	fi
	old=$(image_id "$tag" 2>/dev/null) || old=
	version=$("$go" env GOVERSION)

	mkdir "$work/context"
	cp "$binary" "$work/context/agentswarm"
	docker buildx build --provenance=false --sbom=false \
		--build-arg VERSION=dev --build-arg GO="$version" \
		--output "type=image,name=$tag" \
		-f build/runtime/Dockerfile "$work/context"

	id=$(image_id "$tag")
	echo "image $id"
	if [ -n "$old" ] && [ "$old" != "$id" ]; then
		remove "$old" "$tag"
	fi
}

# remove removes the image $1, which the tag $2 named before, once its docker image inspect
# is saved as .test-clean/<its 64 hex digits>-<UTC time>.inspect.json. As
# build/test/run.sh does, a backup takes its name only once it is whole, and never replaces
# a file. An image that a tag still names stays, since docker image rm by id would untag
# and delete an image that one other tag names; so does one whose backup fails. A removal
# the engine refuses, as when a container uses the image, is printed, and the build stands.
remove() {
	local id=$1 tag=$2 tags file
	if ! tags=$(docker image inspect --format '{{len .RepoTags}}' "$id"); then
		echo "build/runtime/image.sh: $id, which $tag named, is not removed, as the engine cannot inspect it" >&2
		return
	fi
	file=.test-clean/${id#sha256:}-$(date -u +%Y%m%dT%H%M%SZ).inspect.json
	if [ "$tags" != 0 ]; then
		echo "build/runtime/image.sh: $id, which $tag named, stays, as a tag still names it" >&2
	elif [ -e "$file" ] || [ -L "$file" ]; then
		echo "build/runtime/image.sh: $id stays, as $file already exists, and a backup is never replaced" >&2
	elif ! (umask 077 && mkdir -p -m 0750 .test-clean && docker image inspect "$id" >"$file.partial" && mv "$file.partial" "$file"); then
		echo "build/runtime/image.sh: $id stays, as its backup failed" >&2
	elif ! docker image rm "$id" >/dev/null; then
		echo "build/runtime/image.sh: $id is backed up as $file but was not removed" >&2
	else
		echo "build/runtime/image.sh: removed $id, which $tag named; backed up as $file"
	fi
}

# work is the script's own directory, removed on exit: the commit's source, and the build's
# context, which holds the binary alone.
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

case ${1:-} in
commit)
	[ $# -eq 2 ] || usage
	build_commit "$2"
	;;
dev)
	[ $# -eq 4 ] || usage
	build_dev "$2" "$3" "$4"
	;;
remove)
	[ $# -eq 3 ] || usage
	remove "$2" "$3"
	;;
*)
	usage
	;;
esac
