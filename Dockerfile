FROM golang:latest

RUN apt-get update

RUN apt install -y cmake libboost-all-dev git-core protobuf-compiler jq
RUN git clone --recursive https://github.com/ethereum/solidity.git && \
    cd solidity && \
    git submodule update --init --recursive && \
    mkdir -p build && \
    CI=yes ./scripts/build.sh && \
    cd build && make install

COPY go.sum go.mod main.go /go/src/

RUN cd /go/src && \
    go mod tidy && \
    (go build || true) && \
    cd /go/pkg/mod/github.com/ethereum/go-ethereum@v1.10.26 && \
    make devtools

COPY main.go /go/src/

COPY strip_abi.sh \
     Slots.json Coinflip.json Roulette.json \
     JustPool.json \
     /tmp/

RUN chmod +x /tmp/strip_abi.sh && \
    /tmp/strip_abi.sh /tmp/JustPool.json /tmp/Roulette.json /tmp/Slots.json /tmp/Coinflip.json 

RUN /go/bin/abigen --pkg main --type CasinoPool --abi /tmp/JustPoolStriped.json --out /go/src/casinopool.go
RUN /go/bin/abigen --pkg main --type Roulette --abi /tmp/RouletteStriped.json --out /go/src/roulette.go
RUN /go/bin/abigen --pkg main --type Slots --abi /tmp/SlotsStriped.json --out /go/src/slots.go
RUN /go/bin/abigen --pkg main --type Coinflip --abi /tmp/CoinflipStriped.json --out /go/src/coinflip.go

RUN cd /go/src && \
    go build

ENTRYPOINT cd /go/src && ./pool-pnl