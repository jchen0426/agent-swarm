#!/bin/bash
# The runtime image's build (Plan 7d-2, Decision 6, Contract C12), run from the checkout's
# root, as make image and make image-dev run it, with the Docker engine's buildx plugin.
#
# Usage: build/runtime/image.sh commit COMMIT
#        build/runtime/image.sh dev BINARY TAG
#
# commit builds the runtime image of COMMIT, so that one commit gives one image id on one
# engine: the binary from git archive of the commit, static, its version the commit's id
# and its date the commit's time, by the pinned toolchain, for the engine's architecture or
# ARCH's; the image from the checkout's Dockerfile, without the cache or attestations, its
# times the commit's. It tags the image agentswarm-runtime:<its id's 64 hex digits>, leaves
# the build's own name, agentswarm-runtime:<the commit's id>, and prints the binary's
# SHA-256 and the image's id.
#
# dev puts the same Dockerfile around BINARY, the worktree's Linux binary, names the image
# TAG, the worktree's own, and prints its id; it promises no reproducibility. The image TAG
# named before, its own worktree's alone, is then removed after a backup, so that each gate
# does not leave the last one's dangling.
set -eu -o pipefail

module=github.com/jchen0426/agent-swarm
# The toolchain a commit's binary is built with, which its label names.
toolchain=go1.26.0
# The go command, which make passes as its GO.
go=${GO:-go}

usage() {
	echo "usage: build/runtime/image.sh commit COMMIT | dev BINARY TAG" >&2
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

# sha256 prints the SHA-256 of the file $1.
sha256() {
	local sum
	if command -v sha256sum >/dev/null; then
		sum=$(sha256sum "$1")
	else
		sum=$(shasum -a 256 "$1")
	fi
	echo "${sum%% *}"
}

# build_commit builds the runtime image of the commit $1 in the directory $work.
build_commit() {
	local commit epoch stamp date arch id sum
	commit=$(git rev-parse --verify "$1^{commit}")
	epoch=$(git log -1 --format=%ct "$commit")
	stamp=$(TZ=UTC git log -1 --date=format-local:%Y%m%d%H%M.%S --format=%cd "$commit")
	date=$(TZ=UTC git log -1 --date=format-local:%Y-%m-%dT%H:%M:%SZ --format=%cd "$commit")
	arch=${ARCH:-$(docker version --format '{{.Server.Arch}}')}

	mkdir "$work/src" "$work/context"
	git archive "$commit" | tar -x -C "$work/src"
	(
		cd "$work/src"
		GOTOOLCHAIN=$toolchain CGO_ENABLED=0 GOOS=linux GOARCH=$arch "$go" build -trimpath -buildvcs=false \
			-ldflags "-s -w -X $module/internal/versioninfo.version=$commit -X $module/internal/versioninfo.commit=$commit -X $module/internal/versioninfo.date=$date" \
			-o "$work/context/agentswarm" ./cmd/agentswarm
	)
	# The binary's mtime is the commit's time, in UTC, as the layer holds it.
	TZ=UTC touch -t "$stamp" "$work/context/agentswarm"
	# The platform is the binary's, so that an ARCH other than the engine's is the image's too.
	SOURCE_DATE_EPOCH=$epoch docker buildx build --no-cache --provenance=false --sbom=false --platform "linux/$arch" \
		--build-arg VERSION="$commit" --build-arg GO="$toolchain" \
		--output "type=image,name=agentswarm-runtime:$commit,rewrite-timestamp=true" \
		-f build/runtime/Dockerfile "$work/context"

	id=$(image_id "agentswarm-runtime:$commit")
	docker tag "$id" "agentswarm-runtime:${id#sha256:}"
	sum=$(sha256 "$work/context/agentswarm")
	echo "binary sha256:$sum"
	echo "image $id"
}

# build_dev builds the runtime image around the binary $1 in the directory $work, names it
# $2, and removes the image $2 named before.
build_dev() {
	local binary=$1 tag=$2 old version id
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
	[ $# -eq 3 ] || usage
	build_dev "$2" "$3"
	;;
*)
	usage
	;;
esac
