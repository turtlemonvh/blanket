#!/bin/bash
set -e
export PATH=/home/turtl/go/bin:/home/turtl/node-v20.14.0-linux-x64/bin:$PATH

cd /home/turtl/code/blanket
echo "=== Building blanket binary ==="
go build -o blanket .
echo "=== Build complete ==="

cd /home/turtl/code/blanket/tests/e2e
echo "=== Running Playwright tests ==="
npx playwright test --timeout=60000 2>&1
echo "=== Tests complete ==="
