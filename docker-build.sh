#!/usr/bin/env bash

VERSION=${1:-latest}

docker buildx build --platform=linux/amd64 -t mylxsw/openai-dispatcher:$VERSION -t mylxsw/openai-dispatcher:latest . --push