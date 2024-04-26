#!/usr/bin/env bash

VERSION=${1:-latest}

docker buildx build --platform=linux/amd64,linux/arm64 -t mylxsw/openai-dispatcher:$VERSION . --push