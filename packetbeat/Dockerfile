FROM ubuntu:16.04

RUN apt-get update && \
    apt-get install snmpd -y && \
    apt-get install -y python-pip && \
    mkdir /usr/share/packetbeat && \
    mkdir /usr/share/packetbeat/bin && \
    mkdir /etc/packetbeat

COPY statistics_config.json announcement_bam_deploy.py bin/packetbeat /usr/share/packetbeat/bin/
COPY packetbeat.yml /etc/packetbeat/

CMD ["/usr/share/packetbeat/bin/packetbeat", "-c", "/etc/packetbeat/packetbeat.yml", "--path.logs", "/var/log/packetbeat", "--path.home", "/usr/share/packetbeat", "--path.config", "/etc/packetbeat", "--path.data", "/var/lib/packetbeat"]
