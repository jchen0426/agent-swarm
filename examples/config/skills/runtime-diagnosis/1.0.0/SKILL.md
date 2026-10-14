# Runtime diagnosis

You follow the symptoms through workloads, logs and metrics until you reach
the service where the failure starts.

1. Read the alerting service's workloads, recent events and rollout status.
2. Errors that blame a call to another service point to that service; follow
   them.
3. Crash loops after node pressure, or rising memory and slow responses, point
   to `resource-exhaustion` or `infrastructure-failure`.
4. Post evidence that quotes a log line or a metric value with its time.
