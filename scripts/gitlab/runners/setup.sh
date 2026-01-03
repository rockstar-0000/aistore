#!/bin/bash

# This script installs Docker and GitLab Runner if not already installed.

# Must be run as root or with sudo

set -e

SCRIPTS_DIR=$(dirname "$(realpath -s $"0")")
TMP_DOWNLOAD="$SCRIPTS_DIR/tmp_download"
DATA_ROOT=""
RUNNER_VERSION="18.1.1-1"
REGISTRY_PORT=5000

# Source shared utilities
source "$SCRIPTS_DIR/utils.sh"

# Create the directory if it doesn't already exist
if [ ! -d "$TMP_DOWNLOAD" ]; then
    mkdir -p "$TMP_DOWNLOAD"
fi
cd "$TMP_DOWNLOAD"

# Parse arguments
while [[ $# -gt 0 ]]; do
  case "$1" in
    --data-root)
      DATA_ROOT="$2" && shift 2
      ;;
    --data-root=*)
      DATA_ROOT="${1#*=}" && shift
      ;;
    --help|-h)
      echo "Usage: $0 [--data-root <absolute_path>]"
      exit 0
      ;;
    *)
      echo "Unknown parameter: $1"
      echo "Usage: $0 [--data-root <absolute_path>]"
      exit 1
      ;;
  esac
done

# Validate and set DATA_ROOT
if [[ -n "$DATA_ROOT" ]]; then
  [[ "$DATA_ROOT" == /* ]] || { echo "Error: --data-root must be absolute."; exit 1; }
fi

# Install Docker
if ! command -v docker &>/dev/null; then
  [[ -n "$DATA_ROOT" ]] && configure_docker_root_dir
  install_docker
fi

# Install GitLab Runner
if ! command -v gitlab-runner &>/dev/null; then
  install_runner
  [[ -n "$DATA_ROOT" ]] && configure_runner_dirs
fi

configure_inotify_limits

# Set up pull-through registry cache
setup_registry

# Cleanup
cd "$SCRIPTS_DIR"
rm -rf "$TMP_DOWNLOAD" 