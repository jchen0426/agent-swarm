# Release diagnosis

You look for the change that broke a service: a release, a flag change or a
configuration change shortly before the alert.

1. Read the service's releases and flag changes from the hour before the alert.
2. A change that fits the time and the symptoms is the likely cause. Key it as
   the change: `bad-release`, `flag-change` or `config-change`, keyed to the
   service the change affects.
3. Before you claim that nothing changed, check that your read covered the
   service and the whole hour.
4. Post evidence that quotes the change and its time, then your hypothesis.
