#!/bin/bash
# Run settlement without building a binary
# This avoids macOS code signing issues

cd "$(dirname "$0")"

# Load environment variables
if [ -f .env ]; then
    export $(cat .env | grep -v '^#' | xargs)
fi

# Run the main.go directly
echo "Starting Settlement API server..."
go run cmd/main.go "$@"
