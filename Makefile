TOPO    := lab/topology.clab.yml
PREFIX  := clab-open-dci
BIN     := lab/bin
CLAB    ?= containerlab   # CI: CLAB="sudo containerlab"
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint labnode lab-image proto proto-check docs-svg website website-serve lab-up lab-check lab-check-quick lab-perf lab-down lab-redeploy lab-redeploy-quick lab-capture

build:            ## open-dci and dci-controller binaries (static)
	CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(VERSION)" -o $(BIN)/open-dci ./cmd/open-dci
	CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(VERSION)" -o $(BIN)/dci-controller ./cmd/dci-controller

test:             ## unit tests (config, rendering, golden files, lab specs)
	go test ./...

lint:             ## what CI checks besides tests
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	go vet ./...
	go vet -tags e2e ./lab/...
	go vet -tags 'e2e perf' ./lab/...

BUF := go run github.com/bufbuild/buf/cmd/buf@v1.73.0

proto:            ## regenerate the Go code of the gRPC API (api/) and lint it
	$(BUF) lint
	$(BUF) generate

proto-check: proto ## CI: the committed generated code must be up to date
	@git diff --exit-code -- api || { echo "api/: generated code is stale, run make proto"; exit 1; }

docs-svg:         ## regenerate the drawings website/static/img/{packet-flow,logical-view}.svg
	go run ./tools/packetflow

website:          ## build the website (website/build); needs Node and npm
	cd website && npm ci && npm run build

website-serve:    ## serve the website locally with live reload
	cd website && npm ci && npm start

labnode:          ## static container entrypoint used by all lab nodes, and the fake metal-apiserver
	CGO_ENABLED=0 go build -o $(BIN)/labnode ./lab/cmd/labnode
	CGO_ENABLED=0 go build -o $(BIN)/fakeapi ./lab/cmd/fakeapi

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
