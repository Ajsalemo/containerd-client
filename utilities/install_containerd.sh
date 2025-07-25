#!/bin/bash

DEFAULT_CONTAINERD_VERSION=1.6.39
# Download containerd
echo "Downloading containerd from https://github.com/containerd/containerd/releases/download/v$DEFAULT_CONTAINERD_VERSION/containerd-$DEFAULT_CONTAINERD_VERSION-linux-amd64.tar.gz"
wget https://github.com/containerd/containerd/releases/download/v$DEFAULT_CONTAINERD_VERSION/containerd-$DEFAULT_CONTAINERD_VERSION-linux-amd64.tar.gz -O /usr/local/containerd-$DEFAULT_CONTAINERD_VERSION-linux-amd64.tar.gz
# Extract the tar to usr/local
echo "Extracting containerd tar.gz to /usr/local"
sudo tar Cxzvf /usr/local /usr/local/containerd-$DEFAULT_CONTAINERD_VERSION-linux-amd64.tar.gz
# Set up the systemd service so it runs at start
echo "Setting up containerd systemd service"
sudo mkdir -p /etc/systemd/system/containerd.service.d
echo "Downloading containerd.service file into /etc/systemd/system/containerd.service"
wget -q https://raw.githubusercontent.com/containerd/containerd/main/containerd.service \
  -O /etc/systemd/system/containerd.service
# Enable containerd via systemctl
echo "Configuring containerd service with systemctl"
sudo systemctl daemon-reload
sudo systemctl enable --now containerd
echo "systemctl configuration with containerd.service is done"
# Install runc
echo "Downloading runc.."
wget -q https://github.com/opencontainers/runc/releases/download/v1.1.4/runc.amd64 -O /usr/local/bin/runc
sudo chmod +x /usr/local/bin/runc
echo "Installing runc into /usr/local/sbin/runc.."
sudo install -m 755 runc.amd64 /usr/local/sbin/runc
echo "runc installation complete"
# Install cni plugins from the other helper script in ./utilities/install_cni.sh
echo "Installing CNI plugins"
sudo bash ./utilities/install_cni.sh
