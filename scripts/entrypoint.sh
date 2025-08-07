#!/bin/bash
set -e

echo "Starting Bridge POS Service..."
echo "Environment MODE: ${MODE:-not_set}"

# --- Create container directories ---
mkdir -p /opt/een/var/log/point_of_sale
mkdir -p /opt/een/data/point_of_sale/audit_logs
mkdir -p /opt/een/data/point_of_sale/badger_db

# --- Start process manager ---
exec /usr/bin/supervisord -n 