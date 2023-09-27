#!/bin/bash

############################################
#
# Usage: clean.sh
#
############################################

source "$(dirname "$0")/../utils.sh"

# Unmount and clears all virtual block disks.
NEXT_TIER=
rm_loopbacks
NEXT_TIER="_next"
rm_loopbacks

build_dest="${GOPATH}/bin"
if [[ -n ${GOBIN} ]]; then
	build_dest="${GOBIN}"
fi

rm -rf ~/.ais*            # cluster config and metadata
rm -rf ~/.config/ais      # CLI, AuthN (config and DB), AuthN tokens produced via CLI
rm -rf /tmp/ais*          # user data and cluster metadata
rm -f ${build_dest}/ais*  # in particular, 'ais' (CLI), 'aisnode', and 'aisloader' binaries
rm -f ${build_dest}/authn # AuthN executable
