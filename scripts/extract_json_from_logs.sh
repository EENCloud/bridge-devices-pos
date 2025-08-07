#!/bin/bash

set -e

# Configuration
TAR_FOLDER="data/7eleven/register_logs/tar_folder"
RIS_LISTENER_FOLDER="data/7eleven/register_logs/ris-listener"
EXTRACTED_JSONL_FOLDER="data/7eleven/register_logs/extracted_jsonl"

echo "=== POS Log Processing Pipeline ==="

# Step 1: Auto-extract tar files
echo "Step 1: Extracting tar files from $TAR_FOLDER..."
extracted_count=0

for tar_file in "$TAR_FOLDER"/*.tar.gz "$TAR_FOLDER"/*.tar; do
    if [ -f "$tar_file" ]; then
        echo "  Extracting: $(basename "$tar_file")"
        
        # Extract to ris-listener folder
        if [[ "$tar_file" == *.tar.gz ]]; then
            tar -xzf "$tar_file" -C "$RIS_LISTENER_FOLDER" --strip-components=1 2>/dev/null || \
            tar -xzf "$tar_file" -C "$RIS_LISTENER_FOLDER" 2>/dev/null || {
                echo "    Warning: Failed to extract $tar_file"
                continue
            }
        else
            tar -xf "$tar_file" -C "$RIS_LISTENER_FOLDER" --strip-components=1 2>/dev/null || \
            tar -xf "$tar_file" -C "$RIS_LISTENER_FOLDER" 2>/dev/null || {
                echo "    Warning: Failed to extract $tar_file"
                continue
            }
        fi
        
        extracted_count=$((extracted_count + 1))
        echo "    ✓ Extracted successfully"
    fi
done

if [ $extracted_count -eq 0 ]; then
    echo "  No tar files found to extract"
else
    echo "  Extracted $extracted_count tar file(s)"
fi

# Step 2: Process log files to extract JSON
echo "Step 2: Extracting JSON from log files..."
processed_count=0

for log_file in "$RIS_LISTENER_FOLDER"/register_*.log; do
    if [ -f "$log_file" ]; then
        base_name=$(basename "$log_file" .log)
        output_file="$EXTRACTED_JSONL_FOLDER/${base_name}.jsonl"
        
        # Extract JSON lines from log file
        sed -n 's/.*POST request received: //p' "$log_file" > "$output_file"
        
        # Count extracted JSON lines
        json_lines=$(wc -l < "$output_file")
        echo "  Processed $log_file -> $output_file ($json_lines JSON lines)"
        processed_count=$((processed_count + 1))
    fi
done

if [ $processed_count -eq 0 ]; then
    echo "  No log files found to process"
else
    echo "  Processed $processed_count log file(s)"
fi

echo "=== Processing Complete ==="
echo "  Tar files: $TAR_FOLDER"
echo "  Log files: $RIS_LISTENER_FOLDER"  
echo "  JSON files: $EXTRACTED_JSONL_FOLDER"