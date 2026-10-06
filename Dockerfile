# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download all
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/netkit-cni ./cmd/netkit-cni \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/netkit-agent ./cmd/netkit-agent \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/host-local github.com/containernetworking/plugins/plugins/ipam/host-local

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=builder /out /out
ENTRYPOINT ["/out/netkit-agent"]
