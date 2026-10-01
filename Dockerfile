# open-dci container image. It contains vtysh (from the FRR image) and runs
# next to an existing FRR:
#
#   docker run -d --name open-dci --network host --privileged \
#     -v /var/run/frr:/var/run/frr -v /etc/frr:/etc/frr:ro \
#     -v /etc/open-dci:/etc/open-dci:ro -v /var/lib/open-dci:/var/lib/open-dci \
#     ghcr.io/mwindower/open-dci
#
# Keep the FRR base image's major version in line with the FRR it talks to.
ARG FRR_IMAGE=quay.io/frrouting/frr:10.4.1
FROM ${FRR_IMAGE}
COPY open-dci /usr/local/bin/open-dci
ENTRYPOINT ["/usr/local/bin/open-dci"]
CMD ["run", "-c", "/etc/open-dci/config.yaml"]
