#!/bin/bash

set -euo pipefail

DEFAULT_CNI_PLUGIN_COMMIT=v1.1.1
CNI_DIR=/opt/cni

TMPROOT=$(mktemp -d)

install_cni_plugin() {
  local git_repo git_commit tmp_dir

  git_repo="https://github.com/containernetworking/plugins.git"
  git_commit=${1:-$DEFAULT_CNI_PLUGIN_COMMIT}

  tmp_dir=$(mktemp -d '/tmp/cni-plugins.XXXXX')

  # clone and build
  git clone "${git_repo}" "${tmp_dir}/plugins"
  cd "${tmp_dir}/plugins"
  git checkout "${git_commit}"
  ./build_linux.sh

  # install binary
  sudo mkdir -p "${CNI_DIR}"
  sudo cp -r ./bin "${CNI_DIR}"

  sudo rm -rf "${tmp_dir}"
}

install_cni_plugin $@