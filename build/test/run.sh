#!/bin/bash
# The steps make test-linux and make test-docker run in the Linux test image (Plan 7d-2,
# Contract C15), from the checkout's root, as the developer's own uid, with the engine's
# socket: linux runs the whole suite, and docker the Docker runner's path alone.
#
# Usage: build/test/run.sh linux|docker
#
# It makes a new tenant for each go test invocation of the e2e suite, and exports an
# invocation id of its own, AGENTSWARM_IT_RUN, which integration tests put on their
# containers as the label agentswarm-it.run. At its end, whatever its steps did, it removes
# every container of those tenants and of that id, running or not, after a backup, since no
# later sweep lists them again; its exit code stays its steps'.
set -eu

e2e_dir=/srv/agentswarm-e2e

if [ "$(id -u)" -eq 0 ]; then
	echo "build/test/run.sh: refuses to run as root, where the permission tests skip" >&2
	exit 1
fi
if [ ! -d "$e2e_dir" ] || [ ! -w "$e2e_dir" ]; then
	echo "build/test/run.sh: refuses to run, as it cannot write $e2e_dir, which make test-linux makes" >&2
	exit 1
fi

# new_name sets name to its prefix, the UTC time and 8 hex digits from /dev/urandom.
new_name() {
	hex=$(od -An -N4 -tx4 /dev/urandom)
	name=$1-$(date -u +%Y%m%d%H%M%S)-${hex##* }
}

# new_tenant sets tenant to a new tenant for one go test invocation.
new_tenant() {
	new_name e2e
	tenant=$name
	tenants="$tenants $tenant"
}

# remove removes each container labelled $1, running or not, once its docker inspect output
# is saved as .test-clean/<id>-$2.inspect.json; one whose backup fails stays. As make
# test-clean does, a backup takes its name only once it is whole, and never replaces a file.
remove() {
	if ! ids=$(docker ps -a -q --filter "label=$1"); then
		echo "build/test/run.sh: cannot list the containers labelled $1" >&2
		return
	fi
	for id in $ids; do
		file=.test-clean/$id-$2.inspect.json
		if [ -e "$file" ] || [ -L "$file" ]; then
			echo "build/test/run.sh: $id stays, as $file already exists, and a backup is never replaced" >&2
		elif ! (umask 077 && mkdir -p -m 0750 .test-clean && docker inspect "$id" >"$file.partial" && mv "$file.partial" "$file"); then
			echo "build/test/run.sh: $id stays, as its backup failed" >&2
		elif ! docker rm -f -v "$id" >/dev/null; then
			echo "build/test/run.sh: $id is backed up as $file but was not removed" >&2
		else
			echo "build/test/run.sh: removed $id, labelled $1; backed up as $file"
		fi
	done
}

# clean_up runs at the end, whatever the steps did, and exits with their code.
clean_up() {
	status=$?
	set +e
	stamp=$(date -u +%Y%m%dT%H%M%SZ)
	for tenant in $tenants; do
		remove "agentswarm.tenant=$tenant" "$stamp"
	done
	remove "agentswarm-it.run=$AGENTSWARM_IT_RUN" "$stamp"
	exit "$status"
}

new_name run
export AGENTSWARM_IT_RUN=$name
tenants=
trap clean_up EXIT

case ${1:-} in
linux)
	make test test-integration
	new_tenant
	# The binary under test is static, as the release's and make linux-binary's are.
	AGENTSWARM_E2E_TENANT=$tenant CGO_ENABLED=0 make test-e2e BIN=bin/linux/agentswarm
	;;
docker)
	# The Docker runner's integration tests, which build on Linux alone (R14). Each
	# test runs under a tenant of its own, and labels its containers with
	# AGENTSWARM_IT_RUN, so that clean_up removes what its own cleanup never reached.
	go test -race -count=1 -tags integration ./internal/runner/docker/...
	;;
*)
	echo "usage: build/test/run.sh linux|docker" >&2
	exit 2
	;;
esac
