systemd-docker
==============

This is a wrapper for `docker run` so that you can sanely run Docker containers under systemd.  The key thing that this wrapper does is move the container process from the cgroups setup by Docker to the service unit's cgroup.  This handles a bunch of other quirks so please read through documentation to get an understanding of all the implications of running Docker under systemd.

Using this wrapper you can manage containers through `systemctl` or the `docker` CLI and everything should just stay in sync.  Additionally you can leverage all the cgroup functionality of systemd and systemd-notify.

Why I wrote this?
=================

The full context is in [Docker Issue #6791](https://github.com/docker/docker/issues/6791) and this [mailing list thread](https://groups.google.com/d/topic/coreos-dev/wf7G6rA7Bf4/discussion).  The short of it is that systemd does not actually supervise the Docker container but instead the Docker client.  This makes systemd incapable of reliably managing Docker containers without hitting a bunch of really odd situations.

Installation
============

Copy `systemd-docker` to `/opt/bin` (really anywhere you want).  You can download/compile through the normal `go get github.com/ibuildthecloud/systemd-docker`


Quick Usage
===========

Basically, in your unit file use `systemd-docker run` instead of `docker run`.  Here's an example unit file that runs nginx.

```ini
[Unit]
Description=Nginx
After=docker.service
Requires=docker.service

[Service]
ExecStart=/opt/bin/systemd-docker run --rm --name %n nginx
Restart=always
RestartSec=10s
Type=notify
NotifyAccess=all
TimeoutStartSec=120
TimeoutStopSec=15

[Install]
WantedBy=multi-user.target
```

***If you are writing your own unit file, Type=notify and NotifyAccess=all is really important***

Special Note About Named Containers
===================================

In short, it's best to always have `--name %n --rm` in your unit file's `ExecStart`.

The best way I've found to run containers under systemd is to always assign the container a name.  Even better is to put `--name %n` in your unit file and then the name of the container will match the name of the service unit.

If you don't name your container, you will essentially be creating a new container on every start that will get orphaned.  You're probably clever and thinking you can just add `--rm` and that will take care of the orphans.  The problem with this is that `--rm` is not super reliable.  By naming your container, `systemd-docker` will take extra care to keep the systemd unit and the container in sync.  For example, if you do `--name %n --rm`, `systemd-docker` will ensure that the container is really deleted each time.  The issue with `--rm` is that the remove is done from the client side.  If the client dies, the container is not deleted.

If you do `--name %n --rm`, `systemd-docker` on start will look for the named container.  If it exists and is stopped, it will be deleted.  This is really important if you ever change your unit file.  If you change your `ExecStart` command, and it is a named container, the old values will be saved in the stopped container.  By ensuring the container is always deleted, you ensure the args in `ExecStart` are always in sync.

Options
=======

Logging
-------
By default the container's stdout/stderr will be piped to the journal.  If you do not want to use the journal, add `--logs=false` to the beginning of the command.  For example:

`ExecStart=/opt/bin/systemd-docker --logs=false run --rm --name %n nginx`

Environment Variables
---------------------
Using `Environment=` and `EnvironmentFile=`, systemd can set up environment variables for you, but then unfortunately you have to do `run -e ABC=${ABC} -e XYZ=${XYZ}` in your unit file.  You can have the systemd environment variables automatically transfered to your docker container by adding `--env`.  This will essentially read all the current environment variables and add the appropriate `-e ...` flags to your docker run command.  For example:

```
EnvironmentFile=/etc/environment
ExecStart=/opt/bin/systemd-docker --env run --rm --name %n nginx
```

The contents of `/etc/environment` will be added to your docker run command

Cgroups
-------

The main magic of how this works is that the container processes are moved from the Docker cgroups to the system unit cgroups.  By default all application cgroups will be moved.  This means by default you can't use `--cpuset` or `-m` in Docker.  If you don't want to use the systemd cgroups, but instead use the Docker cgroups, you can control which cgroups are transfered using the `--cgroups` option.  **Minimally you must set `name=systemd`; otherwise, systemd will lose track of the container**.  For example


`ExecStart=/opt/bin/systemd-docker --cgroups name=systemd --cgroups=cpu run --rm --name %n nginx`

The above command will use the `name=systemd` and `cpu` cgroups of systemd but then use Docker's cgroups for all the others, like the freezer cgroup.

Pid File
--------

If for whatever reason you want to create a pid file for the container PID, you can.  Just add `--pid-file` as below

`ExecStart=/opt/bin/systemd-docker --pid-file=/var/run/%n.pid --env run --rm --name %n nginx`

systemd-notify support
----------------------

By default `systemd-docker` will send READY=1 to the systemd notification socket.  You can instead delegate the READY=1 call to the container itself.  This is done by adding `--notify`.  For example


`ExecStart=/opt/bin/systemd-docker --notify run --rm --name %n nginx`

What this will do is set up a bind mount for the notification socket and then set the NOTIFY_SOCKET environment variable.  If you are going to use this feature of systemd, take some time to understand the quirks of it.  More info in this [mailing list thread](http://comments.gmane.org/gmane.comp.sysutils.systemd.devel/18649).  In short, systemd-notify is not reliable because often the child dies before systemd has time to determine which cgroup it is a member of

Detaching the client
====================

The `-d` argument to docker has no effect under `systemd-docker`. To cause the `systemd-docker` client to detach after the container is running, simply use `--logs=false --rm=false`. If either `--logs` or `--rm` is true, the `systemd-docker` client will stay alive until it is killed or the container exits.

Running on CoreOS
=================

If you are running on CoreOS, it may be more problematic to install `systemd-docker` to `/opt/bin`.  To make this easier add the following line to your unit file.

`ExecStartPre=/usr/bin/docker run --rm -v /opt/bin:/opt/bin ibuildthecloud/systemd-docker`

That command will install systemd-docker to /opt/bin.  The full nginx example that is above would now be as below.

```ini
[Unit]
Description=Nginx
After=docker.service
Requires=docker.service

[Service]
ExecStartPre=/usr/bin/docker run --rm -v /opt/bin:/opt/bin ibuildthecloud/systemd-docker
ExecStart=/opt/bin/systemd-docker run --rm --name %n nginx
Restart=always
RestartSec=10s
Type=notify
NotifyAccess=all
TimeoutStartSec=120
TimeoutStopSec=15

[Install]
WantedBy=multi-user.target
```

License
-------
[Apache License, Version 2.0](http://www.apache.org/licenses/LICENSE-2.0)
