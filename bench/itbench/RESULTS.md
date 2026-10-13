# What the pipeline found in the data

| | |
|---|---|
| Date | 2026-10-13 |
| Data | ITBench-Lite SRE v0.2 at commit `d0916b08ba421ce5e672e9ad68aa947d938dfef0` |
| Plan | `plan/2026-10-13-plan-3a-mechanism-check-data.md`, with the changes recorded at its end |

## Download

`itbench fetch` checked all 1,572 files of the 13 scenarios, about 11 GB, against the hub's hashes at the commit: SHA-256 for files in LFS, the git blob id for the others. All matched.

## Scenarios

From `itbench prepare` and `itbench baseline`. Times are UTC on 2025-12-15. "Fault" is when the fault began: for a change, its writer's timestamp; for a chaos experiment, its first event in the `chaos-mesh` namespace, which the datasets drop. S34's fault was set inside the running server and left no record.

| Id | Change | Fault | Alert | Changes | Events | Logs | Error spans | Traces end | Baseline, namespace | Baseline, service |
|---|---|---|---|---|---|---|---|---|---|---|
| S4 | yes | 17:48:20 | 17:55:47 | 2 | 9 | 91,164 | 29 | 17:44:17, before the fault | flagd/flag-change, hit | none |
| S5 | yes | 17:47:32 | 17:54:27 | 2 | 12 | 89,104 | 2,667 | 17:54:26 | flagd/flag-change, hit | none |
| S16 | yes | 17:52:47 | 17:57:59 | 1 | 9 | 88,804 | 2,111 | 17:57:58 | shipping/config-change, hit | shipping/config-change, hit |
| S20 | yes | 17:50:05 | 17:55:50 | 1 | 20 | 79,446 | 16,484 | 17:55:49 | product-catalog/bad-release, hit | none |
| S21 | no | 17:53:00 | 17:58:52 | 0 | 6 | 86,997 | 3 | 17:33:07, before the fault | none | none |
| S23 | yes | 17:54:47 | 18:00:13 | 1 | 25 | 76,144 | 14 | 17:42:43, before the fault | checkout/bad-release, hit | none |
| S24 | yes | 17:49:57 | 17:55:17 | 1 | 13 | 80,231 | 19 | 17:38:46, before the fault | checkout/config-change, hit | none |
| S33 | yes | 17:54:51 | 17:59:41 | 2 | 6 | 88,791 | 7 | 17:36:13, before the fault | cart/config-change, hit | none |
| S34 | no | unknown | 17:53:39 | 0 | 2 | 67,172 | 1,335 | 17:53:38 | none | none |
| S80 | no | 17:54:00 | 17:59:26 | 0 | 3 | 99,495 | 1,607 | 17:50:40, before the fault | none | none |
| S81 | no | 17:58:00 | 18:02:48 | 0 | 0 | 76,732 | 10 | 17:44:07, before the fault | none | none |
| S102 | yes | 17:54:21 | 17:59:18 | 1 | 14 | 74,781 | 15 | 17:53:49, before the fault | other/config-change, miss | none |
| S105 | yes | 17:54:13 | 17:59:15 | 1 | 21 | 67,418 | 12 | 17:39:31, before the fault | product-catalog/config-change, hit | none |

The baseline scores 8 of 13 in the namespace scope, 8 of the 9 change scenarios, and 1 of 13 in the service scope. Its S102 answer, `other/config-change`, is one that spec Section 13.4 would leave to raters; the score report marks it, and the check counts it as a miss.

## Findings

- **The trigger.** Each scenario holds exactly one `alerts_in_alerting_state_*.json`: the alerts in alerting state when the benchmark started its agent. Its one to six alerts name up to four services. In S33 the only alert is `PendingPodsDetected`, which names no service, so the service-scoped baseline has nothing to search.
- **Noisy alerts.** `RequestErrorRate` fires above an error rate of 0, so it also fires before the faults, on background errors. Standing alerts such as `KubeClientCertificateExpiration` fire throughout.
- **Every fault that is a change is visible before the alert.** Each has a snapshot after it and at or before the alert. S33 is the closest: ad and cart were snapshotted at 17:59:40.13, less than a second before the alert at 17:59:41. So every stream is cut at the alert, with no look-ahead.
- **The changes in the window are exactly the injections.** The benchmark's own setup writes, such as resetting the flags, fall before the window. A flag fault is the flag change plus a restart of the flag's service a second or two later. The baseline skips restarts, so it names the flag change.
- **S33 changed two Deployments.** ad and cart each got a node selector that matches no node, 8 s apart. The ground truth names only ad. Either counts as a hit, and the baseline names cart, the later one.
- **Deleted pods.** In 8 scenarios a pod was deleted before the alert: the old pod that a rollout replaced, or the pod that S102's fault would not let come back. The last pull before the alert did not list it, so the datasets leave it out, and each has a `SuccessfulDelete` event. Serving its last snapshot would have shown it running and ready.
- **S102's change belongs to no service.** It created a namespace memory quota, and the ad pod could not be recreated under it. The baseline maps it to `other`, which is never a hit.
- **Traces end before the fault in 9 scenarios.** The benchmark's trace tables stop early. Each dataset records where its traces end, and `get_error_spans` returns it with every answer, so that an absence of errors is not read as evidence.
- **Service metrics.** The counters move about once a minute and are scraped every 30 s. A minute that the alert or the table's start cuts short would read as a drop in traffic, so only whole minutes are kept. Repeated rows are skipped: in 7 scenarios every row appears twice, and 3 others repeat a few dozen, and a series that appears after the table's first scrape counts its first value: that is where a fault's first errors are. Within the window, a minute can still read zero and the next one double when a flush falls just past a minute's last scrape: 3 minutes in S20, 1 in S33 and 2 in S80. The tool's description says so.
- **Logs.** All severities are kept. S16's evidence is in INFO logs and in the proxy's access logs, which carry no severity; it has no WARN or ERROR record at all.
- **Values that named the fault.** The injections used names such as `hello-bench-invalid`, `invalid-node` and `invalid-command`. `prepare` renames them to plausible names that fail the same way (`prepare/sanitize.go`).

## Tool answers

`checks/realdata_test.go` calls every tool on all 13 datasets over MCP. On S16, each tool's answer for a typical call is 0.3–6 KB of JSON: `get_changes` 252 bytes, `get_pod_metrics` 6,138 bytes, the others about 2–3 KB.

## Reproducibility

Two runs of `itbench prepare` over the same download wrote the same 130 files, byte for byte.
