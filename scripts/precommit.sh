#!/bin/bash
set -e

echo "Running pre-commit checks..."

# Install act if not present
if ! command -v act &> /dev/null; then
    echo "act not found. Install with: curl https://raw.githubusercontent.com/nektos/act/master/install.sh | sudo bash"
    exit 1
fi

# Run local tests first
echo "Running local tests..."
./scripts/test.sh

# Run GitHub Actions locally with act
echo "Running GitHub Actions locally with act..."
act --rm --pull

echo "Pre-commit checks passed! Ready to push." 