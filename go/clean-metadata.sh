#!/bin/bash
# Clean macOS metadata files that cause Docker build issues

echo "Cleaning macOS metadata files..."

# Find and remove all ._* files
find . -name "._*" -type f -delete

# Remove .DS_Store files
find . -name ".DS_Store" -type f -delete

echo "Cleanup complete!"
echo "Now you can run: docker-compose up --build"
