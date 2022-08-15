FROM golang:1.18 AS build
WORKDIR /build-dir
COPY go.mod .
COPY go.sum .
RUN go mod download all
COPY cmd cmd
RUN go build -o /tmp/dns-bot github.com/kosrk/ton-domain-bot/cmd
RUN git clone https://github.com/startfellows/tongo /tmp/tongo

FROM golang:1.18 AS bot
COPY --from=build /tmp/dns-bot /app/dns-bot
COPY config.json config.json
COPY global-config.json global-config.json
RUN mkdir -p /tongo/lib
COPY --from=build /tmp/tongo/lib/linux/libvm-exec-lib.so /tongo/lib
ENV LD_LIBRARY_PATH=/tongo/lib
CMD ["/app/dns-bot", "-v"]