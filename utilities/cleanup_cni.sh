#!/bin/bash
# Save as cleanup_cni_iptables.sh

echo "Removing CNI iptables rules..."

# Get all CNI chains
CNI_CHAINS=$(sudo iptables-save | grep -E '^:CNI-' | cut -d' ' -f1 | tr -d ':')

for table in nat filter mangle raw; do
    echo "Cleaning table: $table"
    
    # Remove references to CNI chains first
    for chain in $CNI_CHAINS; do
        sudo iptables -t $table -D INPUT -j $chain 2>/dev/null || true
        sudo iptables -t $table -D OUTPUT -j $chain 2>/dev/null || true
        sudo iptables -t $table -D FORWARD -j $chain 2>/dev/null || true
        sudo iptables -t $table -D PREROUTING -j $chain 2>/dev/null || true
        sudo iptables -t $table -D POSTROUTING -j $chain 2>/dev/null || true
    done
    
    # Flush and delete CNI chains
    for chain in $CNI_CHAINS; do
        sudo iptables -t $table -F $chain 2>/dev/null || true
        sudo iptables -t $table -X $chain 2>/dev/null || true
    done
    
    # Remove rules mentioning cni0 or CNI subnets
    sudo iptables-save -t $table | grep -E "(cni0|mynet|10\.10\.0\.0/16|CNI-)" | sed 's/^-A/-D/' | while read rule; do
        sudo iptables -t $table $rule 2>/dev/null || true
    done
done

echo "CNI iptables cleanup complete"