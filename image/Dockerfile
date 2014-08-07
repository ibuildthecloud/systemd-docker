FROM busybox:latest

ADD systemd-docker /
ADD startup.sh /
RUN mkdir -p /opt/bin
CMD ["/startup.sh"]
