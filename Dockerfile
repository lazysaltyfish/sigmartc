# syntax=docker/dockerfile:1

FROM debian:bookworm-slim AS builder

WORKDIR /src

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tar \
    && rm -rf /var/lib/apt/lists/*

COPY go1.25.5.linux-amd64.tar.gz /tmp/go.tgz
RUN tar -C /usr/local -xzf /tmp/go.tgz \
    && rm /tmp/go.tgz

ENV PATH=/usr/local/go/bin:$PATH

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/
COPY web/ web/

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/sigmartc cmd/server/main.go

FROM alpine:3.20

WORKDIR /app

RUN apk add --no-cache ca-certificates

COPY --from=builder /out/sigmartc /app/sigmartc
COPY web/ /app/web/
COPY scripts/docker-entrypoint.sh /app/docker-entrypoint.sh

RUN chmod +x /app/docker-entrypoint.sh

ENV PORT=8080
ENV DATA_DIR=/data

VOLUME ["/data"]

EXPOSE 8080/tcp
EXPOSE 50000/udp

ENTRYPOINT ["/app/docker-entrypoint.sh"]
