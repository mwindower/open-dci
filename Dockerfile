# srv6-dci container image. It contains vtysh (from the FRR image) and runs
# next to an existing FRR:
#
#   docker run -d --name srv6-dci --network host --privileged \
#     -v /var/run/frr:/var/run/frr -v /etc/frr:/etc/frr:ro \
#     -v /etc/srv6-dci:/etc/srv6-dci:ro -v /var/lib/srv6-dci:/var/lib/srv6-dci \
#     ghcr.io/mwindower/srv6-dci
#
# Keep the FRR base image's major version in line with the FRR it talks to.
ARG FRR_IMAGE=quay.io/frrouting/frr:10.6.0
FROM ${FRR_IMAGE}
COPY srv6-dci /usr/local/bin/srv6-dci
ENTRYPOINT ["/usr/local/bin/srv6-dci"]
CMD ["run", "-c", "/etc/srv6-dci/config.yaml"]
