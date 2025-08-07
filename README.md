# Bridge Devices POS

POS transaction ingestion for EEN Bridge. IP→ESN mapping with vendor overrides.

## Modes & Override Behavior

**SIMULATION**: Static baseline from `sim-vendor-config.json`, vendor configs override. Built-in simulator with internal data replay
**TEST**: External data injection, no internal simulator
**PRODUCTION**: No baseline, vendor configs build mappings dynamically  

**ENV VARS**: `MODE=simulation|production|test`, `POS_VERBOSE_LOGGING=true`

```bash
# Simulation with overrides
MODE=simulation ./bridge-devices-pos

# Production (typical deployment)  
MODE=production ./bridge-devices-pos

# Test mode (integration tests)
MODE=test ./bridge-devices-pos

# Docker deployment
docker-compose up -d
# Docker with custom env
MODE=simulation docker-compose up -d

# Testing
make check && scripts/pos_test_script.sh
```

## Quick Commands

```bash
# Build & test
make build && make test

# Config vendor (brand defaults to "seveneleven")
curl -X POST "localhost:33480/pos_config?cameraid=10096352" -d '{"vendors":[{"listen_port":6324,"registers":[{"ip_address":"192.168.1.6","store_number":"38551","terminal_number":"06"}]}]}'

# Send transaction
curl -X POST "localhost:6324/192.168.1.6" -H "X-Register-IP: 192.168.1.6" -d '{"CMD":"StartTransaction"}'

# Check events/metrics  
curl "localhost:33480/events?cameraid=10096352&list=true" | jq length
curl "localhost:33480/metrics" | jq '{uptime: .service.uptime_seconds, events: .database.total_events, esns: .database.active_esns}'

# Check IP→ESN resolution
curl "localhost:33480/register_data" | jq .recent_events[0].remote_addr

# DB cleanup (if locked)
pkill bridge-devices-pos; rm -rf /tmp/bridge-devices-pos/annt_db
```

## Docker Compose Usage

```bash
# Standard deployment
docker-compose up -d

# With environment overrides  
MODE=simulation docker-compose up -d

# Override file usage (docker-compose.override.yml)
# Automatically used for dev/test environments
docker-compose config  # Shows merged configuration
```

## Simulation Config

Edit `data/7eleven/sim-vendor-config.json` for baseline IP→ESN mappings:
```json
{"esn_mappings": {"192.168.1.1": "10000001", "192.168.1.2": "1000face"}}
```
Real vendor configs from bridge settings will override these dynamically.