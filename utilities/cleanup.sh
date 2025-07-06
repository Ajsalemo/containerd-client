#!/bin/bash

# Helper script to clean up containerd tasks and containers
sudo ctr task list -q | xargs -I {} sudo ctr task kill {} && \
    sudo ctr task list -q | xargs -I {} sudo ctr task delete {} && \
    sudo ctr container list -q | xargs -I {} sudo ctr container delete {}

