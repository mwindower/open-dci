TOPO    := lab/topology.clab.yml
PREFIX  := clab-open-dci
BIN     := lab/bin
CLAB    ?= containerlab   # CI: CLAB="sudo containerlab"
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint labnode docs-svg lab-up lab-check lab-perf lab-down lab-redeploy lab-capture

build:            ## open-dci binary (static)
	CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(VERSION)" -o $(BIN)/open-dci ./cmd/open-dci

test:             ## unit tests (config, rendering, golden files, lab specs)
	go test ./...

lint:             ## what CI checks besides tests
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	go vet ./...
	go vet -tags e2e ./lab/...
	go vet -tags 'e2e perf' ./lab/...

docs-svg:         ## regenerate the README drawings docs/packet-flow.svg, docs/logical-view.svg
	go run ./docs/packetflow

labnode:          ## static container entrypoint used by all lab nodes
	CGO_ENABLED=0 go build -o $(BIN)/labnode ./lab/cmd/labnode

lab-up: build labnode   ## deploy the lab; the gateways run open-dci as sidecar
	$(CLAB) deploy -t $(TOPO)

lab-check:        ## e2e tests against the running lab
	go test -tags e2e -count=1 -v -timeout 30m ./lab/

lab-perf:         ## failure semantics under load (gateway/exit fails, ping + TCP), against the running lab
	go test -tags 'e2e perf' -count=1 -v -timeout 30m -run TestPerf ./lab/

lab-down:
	$(CLAB) destroy -t $(TOPO) --cleanup

lab-redeploy: lab-down lab-up lab-check

# SRv6 in VXLAN between gw-a1 and exit-a1 (IPv6 routing header = next header 43)
CAPTURE_NODE ?= exit-a1
CAPTURE_IF   ?= swp3
PING_FROM    ?= m-a
PING_TO      ?= 10.0.32.10
lab-capture:
	docker exec $(PREFIX)-$(CAPTURE_NODE) sh -c 'command -v tcpdump >/dev/null || apk add -q tcpdump'
	docker exec $(PREFIX)-$(CAPTURE_NODE) timeout 5 tcpdump -nni $(CAPTURE_IF) -c 2 'ip6[6] == 43 or (udp port 4789 and udp[8+8+14+6:1] == 43)' & \
	  sleep 1; docker exec $(PREFIX)-$(PING_FROM) ping -c2 -W1 $(PING_TO) >/dev/null; wait
