# Two instances on one host

What a first deployment of the Docker runner copies (Plan 7d-2, Decision 13):
two instances of one tenant on one Linux host, each run by systemd. One holds
the lease and leads; the other stands by, and takes the lease over when the
first stops, so that either can stop or restart while the other leads. The two
share the database, the secrets, the image store, the runtime images and one
clone of the configuration repository. Each keeps its own holder, sockets
directory, output directory, addresses file and admin port.

```
a.yaml, b.yaml                 the deployment files of instances a and b
agentswarm@.service            serve, one unit per instance: agentswarm@a and agentswarm@b
agentswarm-config.service      fetches the configuration repository's main into the shared clone
agentswarm-config.timer        runs the fetch every minute
agentswarm-config-gc.service   packs the clone with git gc
agentswarm-config-gc.timer     runs the gc weekly
```

`agentswarm@a` reads `/etc/agentswarm/a.yaml` and makes `/run/agentswarm/a`,
the sockets directory that `a.yaml` names: an instance's unit, its file and its
sockets directory share its name. `examples/deployment.yaml` says what each key
of a deployment file means, and its Docker variant shows the Docker runner's
block with its optional keys.

## Serve is root on this host

Serve runs in the `docker` group (`SupplementaryGroups=docker`), since it
reaches the engine through the engine's socket. Whoever reaches the engine can
start a container that mounts the host's root file system, so serve's user in
the `docker` group is root on the host in effect, and so is anyone who takes
serve over. Give the platform a host, or a virtual machine, of its own, with
nothing else on it. The sandboxes stay out of that group: serve refuses a
`runner.docker.group` that is the engine socket's group.

## The host

- Debian 12 or Ubuntu 24.04, with Docker Engine 29, on either of the engine's
  image stores, the classic one or containerd's. Serve runs the Docker runner on
  Linux alone, and refuses `runner.kind: docker` on another system.
- SELinux-enforcing hosts are not tested: a container's domain may be refused
  the sockets.
- The engine uses the systemd cgroup driver, these hosts' default, with its
  default parent: `docker info --format '{{.CgroupDriver}}'` prints `systemd`,
  and `/etc/docker/daemon.json` sets no `cgroup-parent`. Each sandbox is then
  `docker-<id>.scope` in `system.slice`. Otherwise `Slice=system.slice` does not
  put serve beside the sandboxes, and its `CPUWeight=` does not weigh serve
  against them.
- The engine's live-restore stays off, Docker's default:
  `docker info --format '{{.LiveRestoreEnabled}}'` prints `false`, and
  `/etc/docker/daemon.json` does not set `live-restore`. An engine stop or
  restart, as an upgrade makes, then stops each sandbox with SIGTERM: the
  runtime ends with 15, and the runner reads the turn `unavailable`, which
  counts against no agent. The agent's turn in that round is over, and the
  agent starts again in the run's next round. With live-restore on,
  turns would outlive an upgrade but lose their output stream, and the runner
  would wait for each again; that is not tested.

## Setting the host up

As root, from this directory, with the `agentswarm` binary for this host at
`/usr/local/bin/agentswarm`:

```sh
useradd --system --user-group --home-dir /var/lib/agentswarm --shell /usr/sbin/nologin agentswarm
install -d -m 0755 /etc/agentswarm
install -d -o agentswarm -g agentswarm -m 0700 /etc/agentswarm/secrets \
    /var/lib/agentswarm /var/lib/agentswarm/images \
    /var/lib/agentswarm/a /var/lib/agentswarm/a/turns \
    /var/lib/agentswarm/b /var/lib/agentswarm/b/turns
install -o root -g agentswarm -m 0640 a.yaml b.yaml /etc/agentswarm/
install -m 0644 agentswarm@.service agentswarm-config.service agentswarm-config.timer \
    agentswarm-config-gc.service agentswarm-config-gc.timer /etc/systemd/system/
```

```
/etc/agentswarm/a.yaml, b.yaml       the deployment files
/etc/agentswarm/secrets/             the secrets, one file per secret
/var/lib/agentswarm/config.git       the bare clone of the configuration repository
/var/lib/agentswarm/images/          the image store, with the instances' locks under holders/
/var/lib/agentswarm/a/, b/           each instance's addresses file and its turns' output
/run/agentswarm/a/, b/               each instance's sockets directory, which its unit makes
```

The directories are agentswarm's alone, mode 0700: the sandboxes share
agentswarm's group, which reaches the sockets.

### The secrets

Each secret is a file under `/etc/agentswarm/secrets`, owned by agentswarm,
mode 0600, which both instances read:

```
database/url   the database's URL: postgres://<user>:<password>@<host>:5432/<database>
admin/token    the admin API's bearer token
vendor/key     the model vendor's API key
mcp/<server>   each MCP server's credential, as its registration's credentialRef names it
```

Write each from where the team keeps it. Never give a value on a command line,
where the host's process list shows it, nor in a deployment file or this
repository. The fetcher's credential for the configuration repository is
agentswarm's own (below).

Serve reads the admin token and the vendor key again at each use. It keeps each
MCP server's credential for at most a minute, then reads it again at its next
use, and reads it again at once when the server refuses it. So a rotated one of
these takes effect without a restart.

Serve reads `database/url` once, at its start, and both of its pools keep it,
so a rotated URL takes effect at each instance's next start. Rotate the
database's credential in this order:

1. Write the new URL while the old credential still works. With PostgreSQL,
   whose role holds one password at a time, that means two roles with the same
   rights.
2. Restart the instances one at a time, each while the other leads.
3. Only then revoke the old credential.

Revoked first, the old credential would leave both instances unable to open a
connection once their pools replace the ones they hold, and so unable to renew
the lease or take it. Serve then stays up as a standby, which `Restart=always`
never restarts.

Before the first start, apply the migrations:

```sh
sudo -u agentswarm agentswarm migrate --secrets /etc/agentswarm/secrets --database secret://database/url
```

### The configuration clone

Both instances read one bare clone, `/var/lib/agentswarm/config.git`, which
one fetcher keeps current, as two clones would lag each other (on two hosts,
each keeps its own). Make it as agentswarm, which the fetcher runs as:

```sh
sudo -u agentswarm git clone --bare <the configuration repository's URL> /var/lib/agentswarm/config.git
sudo -u agentswarm git --git-dir=/var/lib/agentswarm/config.git config gc.auto 0
sudo -u agentswarm git --git-dir=/var/lib/agentswarm/config.git config maintenance.auto false
```

agentswarm's credential for the repository is its own, and no unit names it:
for an SSH URL, a read-only deploy key in `~agentswarm/.ssh/`, mode 0600, with
the remote's host key in its `known_hosts`.

`gc.auto 0` and `maintenance.auto false` keep git from repacking the clone on
its own, which could remove a pack while serve reads it.
`agentswarm-config-gc.timer` runs `git gc` weekly instead; serve opens the
clone again for each read, so it sees the new packs.

`agentswarm-config.timer` runs `agentswarm-config.service` every minute, which
fetches:

```sh
git --git-dir=/var/lib/agentswarm/config.git fetch --no-tags origin refs/heads/main:refs/heads/main
```

The refspec has no `+`, so the fetch is fast-forward only: a force-push of
main fails it, and every fetch after it, visibly, rather than drop commits that
items locked. To take a rewritten main, first keep the old one's commits under
a ref of their own, so that gc keeps what active items locked; then fetch once
by hand, forced. Run both as agentswarm, as the fetcher runs: run as root,
either would leave files of root's in the clone, which fail the fetcher every
minute.

```sh
sudo -u agentswarm git --git-dir=/var/lib/agentswarm/config.git update-ref refs/kept/<date> refs/heads/main
sudo -u agentswarm git --git-dir=/var/lib/agentswarm/config.git fetch --no-tags origin +refs/heads/main:refs/heads/main
```

Both units name `git` bare, which systemd finds on its own search path, in
`/usr/bin`.

### The runtime image

Build each runtime image with `make image`, in a checkout of this repository,
its docker command talking to the engine that runs the sandboxes: on this host,
or through `DOCKER_HOST` or a docker context. It builds the image of
`IMAGE_COMMIT`, `HEAD` by default, from the commit alone, tags it
`agentswarm-runtime:<its id's 64 hex digits>`, and prints its id as
`image sha256:<64 hex digits>`: that id goes in `runner.images.thinloop` of both
deployment files. One commit gives one id on one engine, but another engine's
BuildKit may compress the layers otherwise and give another, so build on the
engine that runs the image. `make image` needs git, Go and the engine's buildx
plugin. At its start, serve refuses an image of `runner.images` that the engine
and the store both lack, or that breaks the image contract.

### The units

```sh
systemctl daemon-reload
systemctl enable --now agentswarm-config.timer agentswarm-config-gc.timer
systemctl enable --now agentswarm@a.service agentswarm@b.service
```

`agentswarm@.service` runs `agentswarm serve --deployment /etc/agentswarm/%i.yaml`:

- `User=agentswarm`, `Group=agentswarm` and `SupplementaryGroups=docker`: the
  engine's socket belongs to the `docker` group (see "Serve is root on this
  host").
- `After=docker.service` and `Wants=docker.service`, not `Requires=` or
  `BindsTo=`, so that an engine restart stops neither instance: their running
  turns read `unavailable`, and their agents start again in the next round.
- `Restart=always`, `RestartSec=5` and `StartLimitIntervalSec=0`: serve refuses
  to start while the engine is down, so systemd tries again every 5 s, with no
  limit. A serve that keeps refusing shows as `serve refused to start`, with the
  key at fault, in its journal (`journalctl -u agentswarm@a`), not as a failed
  unit.
- `TimeoutStopSec=120`: `systemctl stop` sends SIGTERM, which drains serve, and
  systemd kills serve after TimeoutStopSec. It stays above the worst drain,
  which serve logs at its start as `worstDrain` in its `serve started` line:
  `1m27s` with the default timings. Raise it with a timing that raises the worst
  drain past it.
- `CPUWeight=1000` with `Slice=system.slice`: each sandbox's cgroup,
  `docker-<id>.scope` in `system.slice` under Docker's systemd cgroup driver,
  has the default weight, 100, so serve, which every turn's gateway calls reach,
  outweighs each sandbox. Without `Slice=`, systemd would put each instance in
  its template's own slice, `system-agentswarm.slice`, of weight 100, which is
  what each sandbox would then meet: `CPUWeight=` would weigh serve against the
  other instance alone.
- `RuntimeDirectory=agentswarm/%i` with `RuntimeDirectoryMode=0750`: systemd
  makes `/run/agentswarm/%i` at each start, owned by agentswarm, mode 0750, and
  removes it at each stop. That directory meets every check serve makes of
  `runner.docker.sockets` at its start: `a.yaml` names it by its resolved
  path, `/run/agentswarm/a`, not `/var/run/agentswarm/a`, which on Debian and
  Ubuntu goes through `/var/run`, a link to `/run` (every sandbox mounts the
  path as given, which the engine resolves again at each sandbox's creation,
  so serve refuses a path with a link in it, naming the path to give); serve
  owns it; neither its group nor others can write in it; it holds nothing but
  `mcp.sock` and `llm.sock`; its sockets' paths fit the 107 bytes a socket's
  path may hold; and the sandboxes' group, serve's own, can traverse it. Serve
  then holds its flock while it runs. To make the directory by hand instead,
  give it mode 0750 outright
  (`install -d -o agentswarm -g agentswarm -m 0750 /run/agentswarm/a`):
  a umask of 002, Debian's and Ubuntu's for users with a private group, would
  make it 0775, which serve refuses. `/run` is a tmpfs, so a directory made by
  hand there is gone after each boot. For a serve outside systemd, or in a
  container, have systemd-tmpfiles make it at each boot, with a line for each
  instance in `/etc/tmpfiles.d/agentswarm.conf`;
  `systemd-tmpfiles --create /etc/tmpfiles.d/agentswarm.conf` makes them now:

  ```
  d /run/agentswarm/a 0750 agentswarm agentswarm -
  d /run/agentswarm/b 0750 agentswarm agentswarm -
  ```
- No `PrivateTmp=` or `BindPaths=`: the sockets directory's path must be the
  same for serve and for the engine, which mounts it into every sandbox. No
  `LimitNOFILE=`: the system's hard limit is higher than one the unit would set.

`agentswarm-config.service` and `agentswarm-config-gc.service` name
`agentswarm-notify@%n.service` in `OnFailure=`: a unit that the deployment
writes, which tells someone, by the team's own channel, that the fetch or the
gc failed; `%n` makes the failed unit's name its instance. No such unit ships
here: write one, or name the notifier the host has. Without it, systemd logs
that it cannot start the unit, and a failing fetch tells no one.

Each of the two also sets `TimeoutStartSec=`: 5 minutes for the fetch, an hour
for the gc. systemd gives a oneshot no start timeout by default, and a timer
starts nothing while its unit still runs. So a hung fetch would stop every
later one without failing, and the configuration would go stale with no notice.
Bounded, it fails, and `OnFailure=` tells someone.

## What the first deployment checks

No test here runs serve under systemd. Every Docker-runner test (serve's
integration test, the end-to-end suite under Docker and the live gate) runs
serve inside a test container, on the engine's default bridge network, with the
engine's socket mounted. The code and the paths are the same, so the risk is
small, but the first deployment checks these units on its host:

1. Once the `OnFailure=` unit exists,
   `systemd-analyze verify agentswarm@a.service agentswarm-config.service agentswarm-config.timer agentswarm-config-gc.service agentswarm-config-gc.timer`
   finds no problem.
2. After `systemctl start agentswarm@a agentswarm@b`, each journal shows
   `serve started` with its holder and `worstDrain`, and `ls -la /run/agentswarm/a`
   shows a directory of agentswarm's, `drwxr-x---`, that holds `mcp.sock` and
   `llm.sock`, `srw-rw----`. `systemctl status agentswarm@a` shows its cgroup,
   `/system.slice/agentswarm@a.service`, beside the sandboxes',
   `/system.slice/docker-<id>.scope`, which `systemd-cgls` lists while a turn
   runs.
3. `/v1/status` on each admin port, 7403 and 7404, with the admin token, shows
   one instance leading, and a runner whose engine is `ok`, with
   `missingImages` empty.
4. An item's first round runs its agents' turns, whose sandboxes reach the
   gateways through the sockets: if the host's security module refuses them,
   it shows here.
5. `systemctl stop` of the leader shows `serve drains`, then `serve stopped`
   with code 0, within TimeoutStopSec, and the other instance takes the lease
   over.
6. `systemctl restart docker` leaves both instances running: the turns that ran
   end `unavailable`, and their agents start again in the next round.
7. `systemctl start agentswarm-config.service` fetches; a fetch that fails
   starts the `OnFailure=` unit.

## The image store

`/var/lib/agentswarm/images` is `runner.docker.store` of both instances. At its
start, serve saves each image of `runner.images` there as
`<its id's 64 hex digits>.tar`, unless the store holds it already. Under
`holders/` are the instances' locks, a file for each tenant and holder, which
stay after serve exits: leave that directory be. Serve deletes nothing from the
store.

No container holds a runtime image between rounds, so the host's prunes reach
them:

- `docker image prune -a` and `docker system prune -a` delete every runtime
  image.
- A plain `docker image prune` deletes a runtime image that no tag names, such
  as one loaded by hand from the store, whose copy was saved by its id and comes
  back untagged.
- An `until=` filter spares no runtime image, however recently built:
  `make image` dates an image at its commit's time, so the filter takes it once
  its commit is older than the cutoff.

When the engine lacks an image, the runner loads it back from the store, at the
next launch of an item that locked it, or at its minute's check of
`runner.images`; checks that the loaded image has that id; tags it as
`make image` does; and logs `an image was reloaded from the store`. A prune then
costs a reload. An image the store lacks too fails every launch of the items
that locked it, with the reason `image`, until their runs stop, and
`/v1/status` lists it under `missingImages` while `runner.images` names it. To
prune the host's other images, filter the runtime images out by their label:
`docker image prune -a --filter 'label!=io.agentswarm.turn-spec'`.

Back the store up with the database: an item that locked an image gets it back
from the store's copy alone, as a rebuild, on another engine or by another
builder, may give another id.

Changing the engine's image store, from the classic store to containerd's or
back (`docker info --format '{{.DriverStatus}}'` shows
`io.containerd.snapshotter.v1` on containerd's), invalidates every lock. One
build has another id on each store, the config's digest on the classic store
and the manifest's on containerd's, so no locked id comes back, from the
engine, the store's copy or a rebuild. Change it with every item closed, then
rebuild each image with `make image`, and put the new ids in `runner.images`.

### Trimming the store

A copy can go once no item that is not closed locked its image, and no instance
can lock it for a new item. An item locks, at its start, the `runner.images`
that its instance loaded at its own start. So an image is still to keep while:

- `runner.images` of any deployment file that shares the store names it; or
- an instance not restarted since its file changed holds it, as a running
  instance locks what it loaded at its start.

This lists the images that items locked and may still run on, those of every
item that started and whose run has not stopped, in every tenant of the
database:

```sql
SELECT DISTINCT i.value AS image
FROM work_records s
CROSS JOIN LATERAL jsonb_each_text(s.body -> 'started' -> 'images') AS i
WHERE s.kind = 'item_started'
  AND NOT EXISTS (SELECT 1 FROM work_records r
                  WHERE r.tenant_id = s.tenant_id AND r.item_id = s.item_id
                    AND r.kind = 'run_stopped')
ORDER BY 1;
```

It keys on the run, not on the item's state. An item that started, and that
the kill switch left not staffed before its run was created, awaits its close
with no run; a person may promote it, and its run then starts on the images it
locked. An item whose run a close stopped records the stop, and lets its images
go. The list errs on the safe side: it keeps the images of an item closed
before it had a run, even once its reopen window has passed.

Run it with psql, or the team's own client, connected as the secret
`database/url` says, with its password in `~/.pgpass` rather than on the
command line. `<hex>.tar` can go when `sha256:<hex>` is in none of these:
that list; `runner.images` of any deployment file; and the `runner.images` that
an instance not restarted since its file changed loaded at its start.

## Switching from the process runner

Switch a deployment from `runner.kind: process` to `docker` with no item that
may still run a turn on the images it locked. An item keeps the images it
locked when it started, and the process runner's are placeholders that no
engine holds: an item that runs under the Docker runner on them fails every
launch, with a `turn_unstarted` notice on its board, whose reason is `image`,
for each agent each round, until its run stops. This lists the items of the
deployment's tenant, here `example`, that started, whose run has not stopped,
and that are not closed, by the rule the coordinator reads active items by: no
`item_closed` without an `item_reopened` after it.

```sql
SELECT s.item_id
FROM work_records s
WHERE s.tenant_id = 'example' AND s.kind = 'item_started'
  AND NOT EXISTS (SELECT 1 FROM work_records r
                  WHERE r.tenant_id = s.tenant_id AND r.item_id = s.item_id
                    AND r.kind = 'run_stopped')
  AND NOT EXISTS (SELECT 1 FROM work_records c
                  WHERE c.tenant_id = s.tenant_id AND c.item_id = s.item_id
                    AND c.kind = 'item_closed'
                    AND NOT EXISTS (SELECT 1 FROM work_records o
                                    WHERE o.tenant_id = c.tenant_id
                                      AND o.item_id = c.item_id
                                      AND o.kind = 'item_reopened'
                                      AND o.seq > c.seq));
```

A queued item needs no listing: it locks the Docker runner's images when it
starts. An item closed after it started, with no run, is not listed, but can
still be reopened within `lifecycle.reopenWindow` and then promoted onto the
placeholders: do not reopen such an item once you have listed the items, or
switch once its window has passed. A listed item can be closed through the
admin API, `POST /v1/items/{id}/close`, which stops its run, if one runs.

Once it lists none with every instance of the deployment stopped, change each
deployment file as `examples/deployment.yaml`'s Docker variant shows, with the
ids `make image` printed in `runner.images`, and start the instances.

## Serve in a container

The units run serve on the host, the topology these examples recommend. Serve
can run in a container on the same engine instead, as every Docker-runner test
here runs it. For a deployment it then needs:

- the host's network, `--network host`: its admin API listens on loopback
  alone, which only the host's network puts where the host's tools reach it;
- `--init`, so that an init process, not serve, is the container's PID 1,
  passes the stop signal on to serve, and reaps what exits;
- a stop timeout above the worst drain, `--stop-timeout 120`: Docker's default,
  10 s, would kill serve in its drain;
- the engine's socket, at the path of `runner.docker.endpoint`, and its group,
  `--group-add` the gid that `stat -c %g /var/run/docker.sock` prints;
- the sockets directory and the store at the same paths as on the host: the
  engine mounts the sockets directory into every sandbox by its path on the
  host, where serve must listen too, and both instances share the store and its
  locks. On the host, the tmpfiles.d lines above make each sockets directory at
  each boot;
- a user, `--user`, that owns the sockets directory, as serve refuses one it
  does not own, and whose group is not root's: an unset `runner.docker.group`
  is serve's own group, which serve refuses when it is 0;
- the deployment file, the secrets, the output directory, the addresses file's
  directory and the configuration clone, at the paths the deployment file names;
- a restart policy, `--restart always`, as the unit's `Restart=always`: with
  live-restore off, an engine restart stops serve's own container too.

This repository builds no image for serve. One needs the `agentswarm` binary and
the CA certificates against which serve checks the vendor's and the MCP servers'
TLS; the runtime image holds the binary alone.
