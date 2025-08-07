#!/bin/bash

set -e

# Cleanup function
cleanup() {
    local exit_code=$?
    
    # Kill application if still running
    if [[ -n "$APP_PID" ]]; then
        echo "[$(date '+%H:%M:%S')] Stopping application (PID: $APP_PID)..."
        kill "$APP_PID" 2>/dev/null || true
        sleep 2
        # Force kill if still running
        if kill -0 "$APP_PID" 2>/dev/null; then
            echo "[$(date '+%H:%M:%S')] Force killing application..."
            kill -9 "$APP_PID" 2>/dev/null || true
        fi
    fi
    
    # Kill any remaining bridge-devices-pos processes
    pkill -f "bridge-devices-pos" 2>/dev/null || true
    
    # Cleanup temp files
    [[ "$cleanup_temp" == "true" ]] && rm -f "$temp_file" 2>/dev/null || true
    
    echo "[$(date '+%H:%M:%S')] Cleanup complete"
    exit $exit_code
}

# Set up signal handlers for proper cleanup
trap cleanup EXIT INT TERM

# Parse CLI arguments
JSONL_FILE=""
ESN="10096352"
REGISTER_IP="192.168.1.6"
FULL_FILE="false"
SIM_MODE="false"
SHOW_HELP="false"

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --sim)
            SIM_MODE="true"
            shift
            ;;
        --full)
            FULL_FILE="true"
            shift
            ;;
        --help|-h)
            SHOW_HELP="true"
            shift
            ;;
        -*)
            echo "Unknown option $1"
            exit 1
            ;;
        *)
            if [[ -z "$JSONL_FILE" ]]; then
                JSONL_FILE="$1"
            elif [[ "$ESN" == "10096352" ]]; then
                ESN="$1"
            elif [[ "$REGISTER_IP" == "192.168.1.6" ]]; then
                REGISTER_IP="$1"
            fi
            shift
            ;;
    esac
done

# Set default file if not provided
if [[ -z "$JSONL_FILE" ]]; then
JSONL_FILE="data/7eleven/register_logs/extracted_jsonl/register_6.jsonl"
fi

# Config
API_PORT="33480"
VENDOR_PORT="6324"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)

# Output
ANNT_FILE="data/pos-test-results/annts_${TIMESTAMP}.jsonl"
ANALYSIS_FILE="data/pos-test-results/analysis_${TIMESTAMP}.txt"

log() { echo "[$(date '+%H:%M:%S')] $1"; }

if [[ "$SHOW_HELP" == "true" ]]; then
    echo "Usage: $0 [options] [jsonl_file] [esn] [register_ip]"
    echo ""
    echo "Options:"
    echo "  --sim       Enable simulator mode (tests built-in traffic generation with fudging)"
    echo "  --full      Process full file instead of limited lines"
    echo "  --help, -h  Show this help message"
    echo ""
    echo "Arguments:"
    echo "  jsonl_file  JSONL file to process (default: register_6.jsonl)"
    echo "  esn         ESN to use (default: 10096352)"
    echo "  register_ip Register IP (default: 192.168.1.6)"
    echo ""
    echo "Modes:"
    echo "  Default:    External data injection via curl (no fudging)"
    echo "  --sim:      Built-in simulator with timestamp/transaction/terminal fudging"
    echo ""
    echo "Output:"
    echo "  ANNTs:      JSONL format"
    echo "  Analysis:   Summary text file"
    exit 0
fi

log "POS Test: file=$JSONL_FILE esn=$ESN ip=$REGISTER_IP full=$FULL_FILE sim=$SIM_MODE"

if [[ ! -f "$JSONL_FILE" ]]; then
    echo "ERROR: File not found: $JSONL_FILE"
    exit 1
fi

mkdir -p "$(dirname "$ANNT_FILE")"

# Build and start
log "Building..."
go build -v ./cmd/bridge-devices-pos

log "Starting application..."
if [[ "$SIM_MODE" == "true" ]]; then
    export MODE="simulation" POS_VERBOSE_LOGGING="true"
    log "Simulator mode: Built-in traffic generation enabled"
else
    export MODE="test" POS_VERBOSE_LOGGING="true"
    log "External mode: Manual data injection"
fi

./bridge-devices-pos > app.log 2>&1 &
APP_PID=$!
log "Started with PID: $APP_PID"

# Wait for startup
sleep 3

# Verify app is running
if ! kill -0 "$APP_PID" 2>/dev/null; then
    echo "ERROR: Application failed to start"
    cat app.log
    exit 1
fi

# Configure
curl -s -X POST "http://localhost:$API_PORT/pos_config?cameraid=$ESN" \
    -H "Content-Type: application/json" \
    -d '{"vendors": [{"brand": "7eleven", "listen_port": '$VENDOR_PORT', "registers": [{"store_number": "38551", "terminal_number": "06", "ip_address": "'$REGISTER_IP'"}]}]}' > /dev/null

if [[ "$SIM_MODE" == "true" ]]; then
    # Simulator mode: Let it run for a fixed time
    TIMEOUT=30
    log "Simulator mode: Running for $TIMEOUT seconds..."
    
    # Wait and monitor
    for ((i=1; i<=TIMEOUT; i++)); do
        if ! kill -0 "$APP_PID" 2>/dev/null; then
            echo "ERROR: Application stopped unexpectedly"
            cat app.log
            exit 1
        fi
        
        if (( i % 10 == 0 )); then
            log "Simulator running... ${i}s elapsed"
        fi
        sleep 1
    done
    
    log "Simulator timeout reached"
    line_count="simulator-generated"
else
    # External mode: Process JSONL file
    # Check file size and limit if needed
    file_lines=$(wc -l < "$JSONL_FILE")
    max_lines=200
    if [[ $file_lines -gt $max_lines ]]; then
        log "File has $file_lines lines, limiting to first $max_lines for testing"
        temp_file="/tmp/pos_test_$$.jsonl"
        head -n $max_lines "$JSONL_FILE" > "$temp_file"
        JSONL_FILE="$temp_file"
        cleanup_temp=true
    else
        log "Processing $file_lines lines"
        cleanup_temp=false
    fi

    # Process
    line_count=0
    if [[ "$FULL_FILE" == "true" ]]; then
        log "Processing full file (first $max_lines lines)..."
        delay=0.05
    else
        log "Processing sequences..."
        delay=0.02
    fi

    while IFS= read -r line; do
        [[ -z "$line" || "$line" == "{}" ]] && continue
        
        # Check if app is still running
        if ! kill -0 "$APP_PID" 2>/dev/null; then
            echo "ERROR: Application stopped unexpectedly"
            cat app.log
            exit 1
        fi
        
        curl -s -X POST "http://localhost:$VENDOR_PORT/$REGISTER_IP" \
            -H "Content-Type: application/json" \
            -d "$line" > /dev/null
        line_count=$((line_count + 1))
        (( line_count % 50 == 0 )) && log "Sent $line_count transactions"
        sleep $delay
    done < "$JSONL_FILE"

    sleep 3
fi

# Collect ANNTs
log "Collecting ANNTs..."
raw_annts="/tmp/raw_annts_$$.json"
curl -s "http://localhost:$API_PORT/events?drain=true" > "$raw_annts"

log "Converting to JSONL format..."
if [[ -s "$raw_annts" && "$(cat "$raw_annts")" != "[]" ]]; then
    # Raw ANNTs are already in proper format - just convert array to JSONL
    jq -c '.[]' "$raw_annts" > "$ANNT_FILE"
else
    echo "" > "$ANNT_FILE"
fi
rm -f "$raw_annts"

# Simple analysis from collected file  
annt_count=$(wc -l < "$ANNT_FILE" 2>/dev/null || echo "0")
echo "File: $JSONL_FILE" > "$ANALYSIS_FILE"
echo "ESN: $ESN" >> "$ANALYSIS_FILE"
echo "Transactions sent: $line_count" >> "$ANALYSIS_FILE"
echo "ANNTs collected: $annt_count" >> "$ANALYSIS_FILE"
echo "ANNTs file: $ANNT_FILE" >> "$ANALYSIS_FILE"
echo "Timestamp: $TIMESTAMP" >> "$ANALYSIS_FILE"

log "Complete! ANNTs: $ANNT_FILE Analysis: $ANALYSIS_FILE"
