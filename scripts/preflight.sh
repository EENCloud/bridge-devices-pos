#!/bin/bash

set -e

# --- Configuration ---
SERVICE_NAME="bridge-devices-pos"
HOST_DATA_DIR="/opt/een/data/point_of_sale"
HOST_LOG_DIR="/opt/een/var/log/point_of_sale"
COMPOSE_DEST="/opt/een/data/docker-compose/bridge-devices-pos"

echo "======================================"
echo "   Bridge POS Pre-flight Setup"
echo "======================================"

# --- Cleanup old containers ---
echo " Cleaning up old instances..."
docker stop $SERVICE_NAME >/dev/null 2>&1 || true
docker rm $SERVICE_NAME >/dev/null 2>&1 || true

# --- Create host directories ---
echo " Creating host directories..."
mkdir -p "${HOST_DATA_DIR}"/{audit_logs,badger_db}
mkdir -p "${HOST_LOG_DIR}"
mkdir -p "${COMPOSE_DEST}"

# --- Set host permissions ---
chown -R root:root "${HOST_DATA_DIR}" "${HOST_LOG_DIR}" "${COMPOSE_DEST}" 2>/dev/null || true

# --- Copy compose file ---
echo " Copying docker-compose.yml..."
cp ../docker-compose.yml "$COMPOSE_DEST/"

echo " Pre-flight setup complete"
echo " Data Directory: ${HOST_DATA_DIR}"
echo " Log Directory:  ${HOST_LOG_DIR}"
echo " Compose File:   ${COMPOSE_DEST}/docker-compose.yml" 