#!/bin/bash

# Helper script to clean up containerd tasks, containers, and CNI iptables rules
echo "Cleaning up containerd tasks and containers..."

# Kill all tasks first, then delete them, then delete containers
echo "Stopping all running tasks..."
sudo ctr task list -q | xargs -I {} sudo ctr task kill {} 2>/dev/null || true

echo "Deleting all tasks..."  
sudo ctr task list -q | xargs -I {} sudo ctr task delete {} 2>/dev/null || true

echo "Deleting all containers..."
sudo ctr container list -q | xargs -I {} sudo ctr container delete {} 2>/dev/null || true

echo "Cleaning up CNI iptables rules..."
# Call CNI cleanup
bash "$(dirname "$0")/cleanup_cni_iptables.sh"

echo "Complete containerd and CNI cleanup finished."

