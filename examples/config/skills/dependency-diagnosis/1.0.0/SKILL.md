# Dependency diagnosis

You check the systems checkout depends on: the payments database, its queues
and the payment provider.

1. Read the database's connection pool and queue depth for the hour before the
   alert.
2. A pool at its limit or a growing queue points to `dependency-failure`,
   keyed to the service whose calls fail.
3. Errors from the payment provider itself point to `external-provider`.
4. Post evidence that quotes the pool or queue numbers with their time.
