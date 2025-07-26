#!/bin/bash
echo "Starting iptables cleanup..."

# Function to safely remove iptables rules
safe_remove_rule() {
    local table=$1
    local chain=$2
    local rule=$3
    
    if sudo iptables -t "$table" -C "$chain" $rule 2>/dev/null; then
        echo "Removing rule from $table/$chain: $rule"
        sudo iptables -t "$table" -D "$chain" $rule
    fi
}

# Function to safely flush and delete chains
safe_remove_chain() {
    local table=$1
    local chain=$2
    
    if sudo iptables -t "$table" -L "$chain" >/dev/null 2>&1; then
        echo "Flushing chain: $table/$chain"
        sudo iptables -t "$table" -F "$chain" 2>/dev/null || true
        echo "Deleting chain: $table/$chain"
        sudo iptables -t "$table" -X "$chain" 2>/dev/null || true
    fi
}

# Get all CNI chains from iptables-save
echo "Discovering all CNI chains..."
CNI_CHAINS=$(sudo iptables-save | grep -E '^:CNI-' | cut -d' ' -f1 | tr -d ':' | sort -u)

if [ -n "$CNI_CHAINS" ]; then
    echo "Found CNI chains: $CNI_CHAINS"
else
    echo "No CNI chains found in iptables-save output"
fi

# Remove CNI rules from standard chains first
echo "Removing CNI rules from standard chains..."
safe_remove_rule "nat" "POSTROUTING" '-m comment --comment "CNI portfwd requiring masquerade" -j CNI-HOSTPORT-MASQ'

# Enhanced cleanup for all tables
for table in nat filter mangle raw; do
    echo "Cleaning table: $table"
    
    # First pass: Remove all rules that reference CNI chains or contain CNI-related content
    echo "  Removing all CNI-related rules from $table table"
    sudo iptables-save -t "$table" 2>/dev/null | grep -E "(CNI-|cni0|mynet|10\.10\.0\.0/16)" | sed 's/^-A/-D/' | while read -r rule; do
        if [ -n "$rule" ]; then
            echo "    Removing rule: $rule"
            sudo iptables -t "$table" $rule 2>/dev/null || true
        fi
    done
    
    # Second pass: Remove references to CNI chains from standard chains
    for chain in $CNI_CHAINS; do
        echo "  Removing references to $chain from standard chains in $table table"
        sudo iptables -t "$table" -D INPUT -j "$chain" 2>/dev/null || true
        sudo iptables -t "$table" -D OUTPUT -j "$chain" 2>/dev/null || true
        sudo iptables -t "$table" -D FORWARD -j "$chain" 2>/dev/null || true
        sudo iptables -t "$table" -D PREROUTING -j "$chain" 2>/dev/null || true
        sudo iptables -t "$table" -D POSTROUTING -j "$chain" 2>/dev/null || true
        
        # Also try removing any rules that jump to this chain
        sudo iptables-save -t "$table" 2>/dev/null | grep "\-j $chain" | sed 's/^-A/-D/' | while read -r rule; do
            if [ -n "$rule" ]; then
                sudo iptables -t "$table" $rule 2>/dev/null || true
            fi
        done
    done
    
    # Third pass: Get list of CNI chains specific to this table (backup method)
    table_cni_chains=$(sudo iptables -t "$table" -L 2>/dev/null | grep "Chain CNI-" | awk '{print $2}' || true)
    
    # Combine both methods to ensure we get all CNI chains
    all_cni_chains=$(echo -e "$CNI_CHAINS\n$table_cni_chains" | sort -u | grep -v '^$')
    
    # Fourth pass: Flush and delete all CNI chains
    if [ -n "$all_cni_chains" ]; then
        for chain in $all_cni_chains; do
            safe_remove_chain "$table" "$chain"
        done
    fi
done

# Verify cleanup and handle any remaining rules
echo ""
echo "Verification - checking for remaining CNI rules..."
remaining_rules=$(sudo iptables-save | grep -i cni || true)

if [ -n "$remaining_rules" ]; then
    echo "⚠ Found remaining CNI rules, performing final cleanup:"
    echo "$remaining_rules"
    
    # Handle any remaining rules
    echo "Performing final aggressive cleanup..."
    
    # Remove any remaining POSTROUTING rules that reference CNI
    sudo iptables-save -t nat | grep "POSTROUTING.*CNI" | sed 's/^-A/-D/' | while read -r rule; do
        if [ -n "$rule" ]; then
            echo "  Removing: $rule"
            sudo iptables -t nat $rule 2>/dev/null || true
        fi
    done
    
    # Get any remaining CNI chains and force delete them
    remaining_chains=$(sudo iptables-save | grep -E '^:CNI-' | cut -d' ' -f1 | tr -d ':' || true)
    if [ -n "$remaining_chains" ]; then
        echo "  Force removing remaining chains: $remaining_chains"
        for chain in $remaining_chains; do
            for table in nat filter mangle raw; do
                sudo iptables -t "$table" -F "$chain" 2>/dev/null || true
                sudo iptables -t "$table" -X "$chain" 2>/dev/null || true
            done
        done
    fi
    
    # Final verification
    final_check=$(sudo iptables-save | grep -i cni || true)
    if [ -z "$final_check" ]; then
        echo "✓ All CNI iptables rules have been successfully removed after aggressive cleanup."
    else
        echo "⚠ Some persistent CNI rules remain:"
        echo "$final_check"
    fi
else
    echo "✓ All CNI iptables rules have been successfully removed."
fi

echo "Comprehensive CNI iptables cleanup completed."
