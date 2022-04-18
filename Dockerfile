FROM --platform=linux/arm64 golang:1.18 AS builder
WORKDIR /build
ARG TARGETARCH
ENV GOPROXY=https://goproxy.io,direct
COPY . .
RUN GOOS=linux   GOARCH=${TARGETARCH} go build -o kube-node-dns main.go

FROM alpine

COPY --from=builder /build/kube-node-dns /kube-node-dns
CMD /kube-node-dns
