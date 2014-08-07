#!/bin/sh

checksum()
{
	md5sum $1 | awk '{print $1}'
}

if [ ! -e /opt/bin/systemd-docker ] || [ "$(checksum /opt/bin/systemd-docker)" != "$(checksum /systemd-docker)" ]; then
	echo "Installing systemd-docker to /opt/bin"
	cp -pf /systemd-docker /opt/bin
fi
