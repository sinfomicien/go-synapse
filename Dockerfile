FROM golang:1.11 as builder

RUN apt-get update && apt-get install -y \
    xz-utils \
&& rm -rf /var/lib/apt/lists/*

ADD https://github.com/upx/upx/releases/download/v3.94/upx-3.94-amd64_linux.tar.xz /usr/local
RUN xz -d -c /usr/local/upx-3.94-amd64_linux.tar.xz | \
    tar -xOf - upx-3.94-amd64_linux/upx > /bin/upx && \
    chmod a+x /bin/upx

WORKDIR $GOPATH/src/github.com/blablacar/go-synapse
COPY . ./
RUN ./gomake && \
	cp ./dist/synapse-v0-linux-amd64/synapse /


FROM haproxy:1.9

RUN apt-get update && apt-get install -y \
    netcat-openbsd \
&& rm -rf /var/lib/apt/lists/*

COPY --from=builder /synapse /
COPY ./examples/synapse-minimal.yml /synapse.yml
COPY ./examples/haproxy-reload.sh /
RUN chmod +x /haproxy-reload.sh

CMD ["/synapse", "/synapse.yml"]
