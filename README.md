# Bridge Devices POS

POS transaction processor. Converts POS data to ANNTs, routes by IP→ESN mapping.

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