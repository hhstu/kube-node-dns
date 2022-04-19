#!/usr/bin/env bash
docker buildx build -t basefly/kube-node-dns:v0.1  --platform=linux/arm64,linux/amd64   . --push
