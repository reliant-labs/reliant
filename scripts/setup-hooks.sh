#!/bin/bash
# Setup script for git hooks
# Copyright (c) 2025 Reliant Labs. All rights reserved.

echo "Setting up git hooks..."

# Configure git to use the .githooks directory
git config core.hooksPath .githooks

echo "Git hooks configured successfully!"
echo "Pre-commit hook will automatically add copyright headers to modified Go files."
echo ""
echo "To manually add copyright headers, use:"
echo "  ./scripts/add-copyright.sh --all       # Add to all Go files"
echo "  ./scripts/add-copyright.sh --modified   # Add to modified files"
echo "  ./scripts/add-copyright.sh --staged     # Add to staged files"