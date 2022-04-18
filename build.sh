#!/usr/bin/env bash
docker buildx build -t basefly/kube-node-dns  --platform=linux/arm64,linux/amd64   . --push
