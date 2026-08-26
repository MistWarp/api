#!/usr/bin/env bash
set -e

osl compile main.osl

manager="$(cd "$(dirname "$0")" && pwd)/../../rotur_manager.sh"
if [ -x "$manager" ]; then
    "$manager" start mistwarp-api
fi
