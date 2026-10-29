# The stubborn test image (Plan 7d-2, Decision 12): a turn that reads nothing,
# ignores SIGTERM, and sleeps for 120 s at most, as Plan 7d-1's stubborn
# script does (R5), so that only a kill ends it before then, and one a killed
# run leaves does not live until make test-clean. The harness locks it, in
# place of the runtime image, for scenario 7 and scenario 16's stubborn case
# under the Docker runner, and the process runner runs the same script.
#
# It meets the runtime image's contract (Contract C12): busybox's environment
# is PATH alone, and it declares no volume, health check or ONBUILD; the
# label lists the spec's versions, as the runtime image's does, and no label
# begins with agentswarm. make test-linux and make test-docker build it with
# --provenance=false --sbom=false, as the runtime image is built. busybox
# 1.37.0, pinned by its index's digest.
FROM busybox@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec
COPY --chmod=0755 stubborn /stubborn
USER 65532:65532
ENTRYPOINT ["/stubborn"]
LABEL io.agentswarm.turn-spec="1,2"
