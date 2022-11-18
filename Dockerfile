FROM golang:latest

RUN apt-get update

RUN apt install -y cmake libboost-all-dev git-core protobuf-compiler
RUN git clone --recursive https://github.com/ethereum/solidity.git && \
    cd solidity && \
    git submodule update --init --recursive && \
    mkdir -p build && \
    CI=yes ./scripts/build.sh && \
    cd build && make install

COPY go.mod /go/src
COPY go.sum /go/src
COPY main.go /go/src

RUN cd /go/src && \
    go mod tidy && \
    (go build || true) && \
    cd /go/pkg/mod/github.com/ethereum/go-ethereum@v1.10.26 && \
    make devtools

COPY JustPool.json /tmp
RUN /go/bin/abigen --pkg main --type CasinoPool --abi /tmp/JustPool.json --out /go/src/casinopool.go

RUN cd /go/src && \
    go mod tidy && \
    go build

ENTRYPOINT cd /go/src && ./pool-pnl