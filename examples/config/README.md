# Example configuration repository

This is the configuration repository of a fictional team: the checkout team of
Example Shop, from `plan/2026-10-13-example-deployment-profile.md`. Every
name, address and system in it is invented. It shows the layout and every
kind that `agentswarm` loads (spec Section 3), and the end-to-end tests check
it against every loader rule.

```
agents/<name>.yaml             Agent: runtime, model, pinned skills, tool grants, subscriptions
skills/<name>/skill.yaml       Skill: owner and released versions
skills/<name>/<version>/       one version's bundle; SKILL.md comes first
servers/<name>.yaml            McpServer: endpoint, pinned image and tool policy
work-item-types/<name>.yaml    WorkItemType: hypothesis key, budgets, limits, shadow runs
trigger-mappings/<name>.yaml   TriggerMapping: one inbound source's events
identity-sources/<name>.yaml   IdentitySource: a read tool whose output confers authority
redaction-rules/<name>.yaml    RedactionRule: patterns and selectors to redact
yanks.yaml                     YankList: withdrawn skill versions
```

Check it with:

```
agentswarm registry validate --dir examples/config
```

The image digests and tool schema hashes are placeholders. A real
registration pins the digest of the server's image, and the hash that
`agentswarm registry hash-tools --endpoint URL` prints for each tool once a
reviewer has read the tool's schema.

To withdraw a skill version, add it to `yanks.yaml`:

```yaml
yanks:
  - skill: release-diagnosis@1.0.0
    by: avery@example.com
    at: 2026-10-14T09:00:00Z
    reason: The rollback steps skip the flag check.
```
