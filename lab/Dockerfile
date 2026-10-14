# The lab's node image: the FRR image every lab node runs (routers, gateways,
# machines), plus the tools the e2e tests need, so they install nothing at
# runtime: tcpdump (does a forged packet reach a machine?), nftables (the
# exits' edge filter counters), iperf3 (make lab-perf). make lab-up builds it
# (docker caches it); keep FRR_IMAGE in line with the tested FRR version.
ARG FRR_IMAGE=quay.io/frrouting/frr:10.4.1
FROM ${FRR_IMAGE}
RUN apk add --no-cache tcpdump nftables iperf3
