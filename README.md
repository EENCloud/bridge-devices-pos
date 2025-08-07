# Bridge Devices POS

POS transaction processor. Converts POS data to ANNTs and sends them, routes by IP→ESN mapping.

## Quick Test - Register message transformation to lquery format

Test the complete POS logs → ANNTs transformation:

```bash
# 1. Clone repo
git clone <repo-url>
cd bridge-devices-pos

# 2. Extract sample data from tar files
# Place your register_logs_*.tar.gz files in data/7eleven/register_logs/tar_folder/
./scripts/extract_json_from_logs.sh

# 3. Run test script with sample file (10 transactions)
./scripts/pos_test_script.sh data/7eleven/register_logs/extracted_jsonl/test_sample.jsonl

# OR use default file (200 transactions from register_5.jsonl)
./scripts/pos_test_script.sh

# OR process entire jsonl file with --full flag
./scripts/pos_test_script.sh --full

# 4. Check results
cat data/pos-test-results/analysis_<timestamp>.txt    # Summary stats
head -n 50 data/pos-test-results/annts_<timestamp>.json | jq .
```

**What you'll see:**
- **Small sample** (test_sample.jsonl): 10 transactions → ~3 ANNTs
- **Default file** (register_5.jsonl): 200 transactions → ~125 ANNTs
- **Full processing** (--full flag): Should process entire file
- **Output types**: NS 92 (Sub transactions), NS 91 (Complete transactions)


## Build & Run

```bash
# Build
docker build -t bridge-devices-pos .

# Run 
docker-compose up -d

# Check status
curl localhost:33480/metrics | jq '.service.mode'

# Quality checks
make check
```

## Configure POS (Required First)

```bash
# Send config to bridge - maps IP to ESN
curl -X POST "localhost:33480/pos_config?cameraid=10096352" \
  -H "Content-Type: application/json" \
  -d '{"7eleven_registers":[{"store_number":"38551","ip_address":"192.168.1.6","port":6324,"terminal_number":"01"}]}'
```

## API Endpoints

```bash
# Get ANNTs for ESN
curl "localhost:33480/events?cameraid=10096352&list=true" | jq '.[0:3]'

# System metrics
curl localhost:33480/metrics | jq .

# Raw register data
curl localhost:33480/register_data | jq .

# Lua driver script
curl localhost:33480/driver
```

## Send Test Transaction

```bash
# Send transaction data (after pos_config above)
curl -X POST "localhost:6324/192.168.1.6" \
  -H "X-Register-IP: 192.168.1.6" \
  -H "Content-Type: application/json" \
  -d '{"CMD":"StartTransaction","metaData":{"storeNumber":"38551","terminalNumber":"06"}}'

curl -X POST "localhost:6324/192.168.1.6" \
  -H "X-Register-IP: 192.168.1.6" \
  -H "Content-Type: application/json" \
  -d '{"CMD":"EndTransaction"}'
```

## Data Processing

```bash
# Extract tar files → logs → JSON
./scripts/extract_json_from_logs.sh

# Restart with fresh simulation data
docker-compose restart point_of_sale
```

## Modes

- **simulation**: Uses `data/7eleven/sim-vendor-config.json` + real configs override
- **production**: Real configs only, no simulation data

## Troubleshooting

```bash
# Logs
docker logs point_of_sale


# Check routing
curl localhost:33480/metrics | jq '.routing'
```