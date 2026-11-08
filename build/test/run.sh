#!/bin/bash
# The steps make test-linux and make test-docker run in the Linux test image (Plan 7d-2,
# Contract C15), from the checkout's root, as the developer's own uid, with the engine's
# socket: linux runs the whole suite, and docker the Docker runner's path alone.
#
# Usage: build/test/run.sh linux|docker
#
# It makes a new tenant for each go test invocation of the e2e suite, and exports an
# invocation id of its own, AGENTSWARM_IT_RUN, which integration tests put on their
# containers and images as the label agentswarm-it.run. At its end, whatever its steps did,
# it removes every container of those tenants and of that id, running or not, and then every
# image of that id, each after a backup, since no later sweep lists them again, and each of
# those tenants' directories under /srv/agentswarm-e2e that is empty; its exit code stays its
# steps'.
set -eu

e2e_dir=/srv/agentswarm-e2e

# gone_scenarios are the scenarios that check a process is gone, which make test-linux runs
# under the process runner too, by name (R27): scenarios 7, 8, 9 and 16, the second signal's,
# and the harness's three tests of its parent.
gone_scenarios=(
	TestATurnThatNeverEndsIsKilledAtItsDeadline
	TestAStandbyTakesOverFromAKilledLeader
	TestAPartitionedLeaderSendsNothingAfterTheStandbysTake
	TestKillingTheHarnessParentLeavesNothingRunning
	TestASecondSignalKillsEveryTurnAndExitsAtOnce
	TestKillingTheParentClosesItsChildsStandardInput
	TestClosingTheParentsOwnInputEndsItAndItsChild
	TestAParentKillsAChildThatDoesNotEndWhenItsInputCloses
)

# web_scenarios are the web interface's scenarios, 26 to 28 (Plan 9a-1), which run under the
# process runner alone (R-c) and skip under Docker: make test-linux runs them with
# gone_scenarios, by name.
web_scenarios=(
	TestAResponderSignsInAndStopsEveryRunFromTheWeb
	TestADrainingLeadersWebRecordsTheOnForTheNextLeader
	TestAnAdminReauthenticatesToTurnTheSwitchOffAndAViewerCannotTurnItOn
)

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

# remove_images removes each image labelled $1, tagged or not, once its docker image inspect
# output is saved as .test-clean/<its 64 hex digits>-$2.inspect.json, as
# build/runtime/image.sh saves one; a backup takes its name only once it is whole, and never
# replaces a file. The label is an invocation's, so every tag of such an image is a test's,
# and goes with it. One whose backup fails, or that the engine will not remove, stays, and
# the script says so. This engine's docker image ls lists an untagged image only with -a.
remove_images() {
	local listed id file
	if ! listed=$(docker image ls -a -q --no-trunc --filter "label=$1"); then
		echo "build/test/run.sh: cannot list the images labelled $1" >&2
		return
	fi
	# An image is listed once for each of its tags.
	for id in $(printf '%s\n' "$listed" | sort -u); do
		if [[ ! $id =~ ^sha256:[0-9a-f]{64}$ ]]; then
			echo "build/test/run.sh: the image '$id', labelled $1, stays, as its id is not sha256: and 64 hex digits" >&2
			continue
		fi
		file=.test-clean/${id#sha256:}-$2.inspect.json
		if [ -e "$file" ] || [ -L "$file" ]; then
			echo "build/test/run.sh: the image $id stays, as $file already exists, and a backup is never replaced" >&2
		elif ! (umask 077 && mkdir -p -m 0750 .test-clean && docker image inspect "$id" >"$file.partial" && mv "$file.partial" "$file"); then
			echo "build/test/run.sh: the image $id stays, as its backup failed" >&2
		elif ! remove_image "$id"; then
			echo "build/test/run.sh: the image $id is backed up as $file but was not removed" >&2
		else
			echo "build/test/run.sh: removed the image $id, labelled $1; backed up as $file"
		fi
	done
}

# remove_image removes the image $1 by its id: first each of its own tags, as docker image
# rm refuses, but for --force, an id that more than one tag names; then the image, if no
# untagging took it already. Nothing is forced, so an image a container uses stays, and
# --no-prune keeps every other image, an untagged parent included.
remove_image() {
	local tags tag
	tags=$(docker image inspect --format '{{range .RepoTags}}{{.}} {{end}}' "$1") || return
	for tag in $tags; do
		docker image rm --no-prune "$tag" >/dev/null || return
	done
	if docker image inspect "$1" >/dev/null 2>&1; then
		docker image rm --no-prune "$1" >/dev/null
	fi
}

# remove_tenant_dir removes the tenant $1's directory under $e2e_dir when it is empty: the
# harness makes it, and an instance's sockets directory in it, which the harness removes at
# the test's end, but nothing removes the tenant's. An empty directory holds nothing to back
# up; one that holds anything stays, and the script names it.
remove_tenant_dir() {
	local dir=$e2e_dir/$1 held
	[ -d "$dir" ] || return 0
	if ! held=$(ls -A "$dir"); then
		echo "build/test/run.sh: $dir stays, as it cannot be listed" >&2
	elif [ -n "$held" ]; then
		echo "build/test/run.sh: $dir stays, as it is not empty: $(tr '\n' ' ' <<<"$held")" >&2
	elif ! rmdir "$dir"; then
		echo "build/test/run.sh: $dir stays, as it could not be removed" >&2
	fi
}

# clean_up runs at the end, whatever the steps did, and exits with their code. It removes
# the containers first, so that none of them still uses an image of the invocation's.
clean_up() {
	status=$?
	set +e
	stamp=$(date -u +%Y%m%dT%H%M%SZ)
	for tenant in $tenants; do
		remove "agentswarm.tenant=$tenant" "$stamp"
		remove_tenant_dir "$tenant"
	done
	remove "agentswarm-it.run=$AGENTSWARM_IT_RUN" "$stamp"
	remove_images "agentswarm-it.run=$AGENTSWARM_IT_RUN" "$stamp"
	exit "$status"
}

# e2e_docker runs the e2e suite under the Docker runner (Decision 12), with a new tenant. The
# binary under test is static, as make build builds it, and as the release's and make
# linux-binary's are.
e2e_docker() {
	new_tenant
	AGENTSWARM_E2E_RUNNER=docker AGENTSWARM_E2E_TENANT=$tenant make test-e2e BIN=bin/linux/agentswarm
}

# check_gone_scenarios fails, naming each, unless every name of gone_scenarios and
# web_scenarios is a test of the e2e suite, which go test lists by the pattern $1: go test
# passes a -run pattern whose name matches no test, so a misspelt or renamed scenario would
# leave the gate in silence.
check_gone_scenarios() {
	local listed name missing=
	listed=$(AGENTSWARM_BIN=$PWD/bin/linux/agentswarm go test -tags e2e -list "$1" ./test/e2e/)
	for name in "${gone_scenarios[@]}" "${web_scenarios[@]}"; do
		if ! grep -qx -- "$name" <<<"$listed"; then
			missing="$missing $name"
		fi
	done
	if [ -n "$missing" ]; then
		echo "build/test/run.sh: no test of the e2e suite is named$missing" >&2
		return 1
	fi
}

# e2e_process_gone runs, under the process runner, with a new tenant, the scenarios that check
# a process is gone (R27) and the web's (web_scenarios), once their names are checked, on the
# binary make test-e2e built.
e2e_process_gone() {
	local pattern
	pattern="^($(IFS='|'; echo "${gone_scenarios[*]}|${web_scenarios[*]}"))\$"
	check_gone_scenarios "$pattern"
	new_tenant
	AGENTSWARM_E2E_RUNNER=process AGENTSWARM_E2E_TENANT=$tenant AGENTSWARM_BIN=$PWD/bin/linux/agentswarm \
		go test -count=1 -timeout 60m -tags e2e -run "$pattern" ./test/e2e/
}

# Sourced, as a check of clean_up does to run it alone, the script defines its functions
# and stops here; run, it goes on.
[[ ${BASH_SOURCE[0]} == "$0" ]] || return 0

if [ "$(id -u)" -eq 0 ]; then
	echo "build/test/run.sh: refuses to run as root, where the permission tests skip" >&2
	exit 1
fi
if [ ! -d "$e2e_dir" ] || [ ! -w "$e2e_dir" ]; then
	echo "build/test/run.sh: refuses to run, as it cannot write $e2e_dir, which make test-linux makes" >&2
	exit 1
fi

new_name run
export AGENTSWARM_IT_RUN=$name
tenants=
trap clean_up EXIT

case ${1:-} in
linux)
	make test test-integration
	e2e_docker
	e2e_process_gone
	;;
docker)
	# The Docker runner's integration tests, which build on Linux alone (R14). Each
	# test runs under a tenant of its own, and labels its containers with
	# AGENTSWARM_IT_RUN, so that clean_up removes what its own cleanup never reached.
	go test -race -count=1 -tags integration ./internal/runner/docker/...
	# Serve's tests of the Docker runner, which start serve with it.
	go test -race -count=1 -tags integration -run Docker ./cmd/agentswarm/
	e2e_docker
	;;
*)
	echo "usage: build/test/run.sh linux|docker" >&2
	exit 2
	;;
esac
