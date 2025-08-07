-- internal/bridge/point_of_sale.lua

-- =============================================================================
-- Global State
-- =============================================================================
local Esn = 0
local EsnStr = ""
local ScriptName = "point_of_sale" -- luacheck: ignore (required by EEN runtime)
local Running = false
local Settings = nil
local Subscriptions = {}

local POS_CONTAINER_PORT = 33480
local POS_SERVICE_URL = string.format("http://localhost:%d", POS_CONTAINER_PORT)
local CONFIG_ENDPOINT = POS_SERVICE_URL .. "/pos_config"
local EVENTS_ENDPOINT = POS_SERVICE_URL .. "/events"

local E = {}
E.Curl = nil
E.Active = false
E.Timeout = 10
E.Count = 0
E.PollingLogged = false

-- =============================================================================
-- Utility Functions
-- =============================================================================

local function confirm_call(resp)
	if not resp then
		return false
	end
	return resp:path("response") == 200
end

-- =============================================================================
-- ANNT Polling Logic
-- =============================================================================
local function process_single_annt(event_data)
	if not event_data then
		return false
	end

	local ok, err = pcall(function()
		-- Get required data or fail
		local mpack_data = event_data:path("mpack")
		if not mpack_data then
			error("Missing mpack data")
		end

		local fields = {
			cameraid = Esn,
			ns = tonumber(event_data:path("ns")),
			op = EENETag.ANNT_OP_ADD,
			flags = EENETag.ANNT_FLAG_BRIDGE,
			mpack = MPack.pack(mpack_data),
			uuid = event_data:path("uuid"),
			seq = tonumber(event_data:path("seq")),
		}

		-- Create and send ANNT (use EEN timestamp for now)
		local timestamp = EENEnv.ts("now")
		local annt = EENETag.new("ANNT", timestamp, EENETag.ANNT_FLAG_BRIDGE, fields)
		annt:send()
	end)
	if ok then
		local ns = event_data:path("ns")
		EENLog.info("POS ANNT: Successfully sent for ns " .. tonumber(ns))
		return true
	else
		EENLog.error("Failed to send POS ETag: " .. tostring(err))
		return false
	end
end

local function get_events_callback(c2, http_result, curl_result)
	E.Curl = nil
	EENLog.info("POS HTTP: %d (curl: %d)", http_result, curl_result)

	if http_result == 200 then
		EENEnv.after(get_events, 0.1)
		local response_body = c2:body()
		if response_body and response_body ~= "" then
			local event_data = MPack.parse(response_body)
			if event_data then
				if not E.PollingLogged then
					EENLog.info("Started processing ANNT events from POS container")
					E.PollingLogged = true
				end

				local process_ok = process_single_annt(event_data)
				if not process_ok then
					EENLog.info("Error processing ANNT event")
				end
			else
				EENLog.info("Failed to parse ANNT response")
			end
		end
	elseif http_result == 204 then
		EENEnv.after(get_events, 0.1)
	else
		EENLog.info("HTTP error " .. http_result .. " (count: " .. E.Count .. "), backing off")
		EENEnv.after(get_events, 10)
	end
end

function get_events()
	if not E.Active then
		return
	end

	local ok, err = pcall(function()
		local url = EVENTS_ENDPOINT .. "?cameraid=" .. EsnStr .. "&bridge=true"
		local headers = {
			["Accept"] = "application/json",
		}
		E.Curl = EENCurl.new(url, headers, get_events_callback)
		E.Curl:timeout(E.Timeout)
		E.Curl:start()
		E.Count = E.Count + 1
	end)

	if not ok then
		EENLog.info("Error starting HTTP request: " .. tostring(err))
		EENEnv.after(get_events, 10)
	end
end

-- =============================================================================
-- Core Logic
-- =============================================================================
--[[
Expected active_scripts structure:
"active_scripts": {
    "v": {
        "point_of_sale": {
            "7eleven_registers": [
                {
                    "store_number": "38551",
                    "ip_address": "192.168.1.1",
                    "port": 6334,
                    "terminal_number": "01"
                }
            ]
        }
    }
}
--]]
local function get_pos_config()
	return Settings:path("data.active_settings.active_scripts.v.point_of_sale")
end

local function send_config_to_pos_service()
	local ok, err = pcall(function()
		local pos_config = get_pos_config()
		if not pos_config then
			return
		end

		local payload = pos_config
		local body_mpack = MPack.pack(payload)
		local body_json = body_mpack:render()

		local config_url = CONFIG_ENDPOINT .. "?cameraid=" .. EsnStr
		local headers = { ["Content-Type"] = "application/json" }
		local curl = EENCurl.new(config_url, headers, function(_, http_result)
			if http_result == 200 then
				EENLog.info("Successfully posted configuration to POS container for ESN " .. EsnStr)
			else
				EENLog.info("Failed to post configuration for ESN " .. EsnStr .. ", HTTP: " .. http_result)
			end
		end)
		curl:set_method("POST")
		curl:set_body(body_json)
		curl:timeout(5)
		curl:start()
	end)

	if not ok then
		EENLog.info("Error sending config to POS service: " .. tostring(err))
	end
end

local function sync_settings()
	local settings_ok, settings = pcall(EENInfo.settings_get, Esn)
	if not settings_ok or not confirm_call(settings) then
		EENLog.info("Could not retrieve valid settings for " .. EsnStr)
		return
	end
	Settings = settings

	send_config_to_pos_service()

	local pos_config = get_pos_config()

	if pos_config and not E.Active then
		EENLog.info("Starting POS ETag polling")
		E.Active = true
		EENEnv.after(get_events, 1)
	elseif not pos_config and E.Active then
		EENLog.info("Stopping POS ETag polling")
		E.Active = false
	end
end

local function settings_callback()
	sync_settings()
end

-- =============================================================================
-- Bridge-core Lifecycle Functions
-- =============================================================================
function install(args)
	local esn = args:path("esn")
	local info = EENInfo.esn_get(esn)
	local device_type = info:path("data.type")
	if device_type == "camera" then
		return EENEnv.INSTALL_FLAG_VISIBLE
	end
	return 0
end

function schema()
	local s = {}
	return MPack.pack(s)
end

local function start_processing()
	-- hook into global table so it does not get gc'd
	if Subscriptions.scripts_hook == nil then
		local scripts_hook = EENInfo.settings_sub(settings_callback, string.format("%08x:settings:active_scripts", Esn))
		Subscriptions.scripts_hook = scripts_hook
	end
	-- camera turn on or off
	if Subscriptions.on_hook == nil then
		local on_hook = EENInfo.settings_sub(settings_callback, string.format("%08x:settings:camera_on", Esn))
		Subscriptions.on_hook = on_hook
	end
	sync_settings() -- and kick us off
end

local function restart()
	local settings = EENInfo.settings_get(Esn)
	if not confirm_call(settings) then
		EENLog.info("Settings not ready for " .. EsnStr)
		return
	end
	Settings = settings
	start_processing()
end

function start(args)
	if Running then
		return
	end

	local esn_ok, esn = pcall(args.path, args, "esn")
	if not esn_ok then
		return
	end
	Esn = esn
	EsnStr = string.format("%08x", Esn)

	EENLog.prefix = EsnStr
	EENLog.level = EENLog.LEVEL_INFO

	EENLog.info("Starting POS container management for " .. EsnStr)
	Running = true
	restart()
end

function stop()
	EENLog.info("Stopping POS container management")
	Running = false
	E.Active = false
	if Subscriptions.scripts_hook then
		Subscriptions.scripts_hook:drop()
		Subscriptions.scripts_hook = nil
	end
end
