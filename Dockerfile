# syntax=docker/dockerfile:1

FROM golang:1.25.6-alpine AS builder

WORKDIR /src

RUN apk add --no-cache ca-certificates

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
