# The runtime image (Plan 7d-2, Decision 6, Contract C12): the agentswarm binary alone,
# which runs a turn as 65532:65532. build/runtime/image.sh builds it: from a commit,
# reproducibly (make image), or around the worktree's Linux binary (make image-dev). The
# COPY sets the binary's mode, so that the builder's umask makes no other layer. A unit
# test of internal/runner/spec keeps the turn-spec label, the versions the runtime reads,
# equal to spec.Reads.
FROM scratch
ARG VERSION
ARG GO
COPY --chmod=0755 agentswarm /agentswarm
USER 65532:65532
ENTRYPOINT ["/agentswarm", "turn"]
LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${VERSION}" \
      org.opencontainers.image.source="https://github.com/jchen0426/agent-swarm" \
      io.agentswarm.turn-spec="1,2" \
      io.agentswarm.go="${GO}"
