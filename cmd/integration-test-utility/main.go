package main

import (
	"bridge-devices-pos/internal/api"
	"bridge-devices-pos/internal/core"
	"bridge-devices-pos/internal/settings"
	"bridge-devices-pos/internal/vendors"
	"bridge-devices-pos/internal/vendors/seveneleven"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/eencloud/goeen/log"
)

func main() {
	fmt.Println("=== Integration Test: Simulation vs Production Modes ===")

	customFormat := "{{eenTimeStamp .Now}}[{{.Level}}][{{.Function}}({{.Filename}}:{{.LineNo}})]: {{.Message}}"
	customContext := log.NewContext(os.Stderr, customFormat, log.LevelInfo)
	logger := customContext.GetLogger("integration-test", log.LevelInfo)

	fmt.Println("\n1. Testing SIMULATION Mode...")
	testSimulationMode(logger)

	fmt.Println("\n2. Testing PRODUCTION Mode...")
	testProductionMode(logger)

	fmt.Println("\n=== Integration Test Complete ===")
}

func testSimulationMode(logger *log.Logger) {
	os.Setenv("MODE", "simulation")
	defer os.Unsetenv("MODE")

	dbPath := filepath.Join("data", "test_sim.db")
	defer os.Remove(dbPath)

	// Create ANNT store for integration test
	anntStore, err := core.NewANNTStore("data/test_annt_integration.db", 1, logger)
	if err != nil {
		logger.Fatalf("Failed to create ANNT store: %v", err)
	}
	defer anntStore.Close()

	settingsManager := settings.NewManager(logger)

	var activeVendor vendors.Vendor
	settingsManager.SetUpdateCallback(func(esn string, vendor *settings.VendorConfig) {
		if activeVendor != nil {
			activeVendor.HandleESNConfiguration(esn, vendor)
		}
	})

	vendorGetter := func() api.Vendor {
		if activeVendor != nil {
			return activeVendor
		}
		return nil
	}

	apiServer := api.NewServer(":0", logger, settingsManager, anntStore, vendorGetter)

	go func() {
		if err := apiServer.Start(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("API Server failed: %v", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	vendorConfig := json.RawMessage(`{
		"brand": "7-eleven",
		"listen_port": 6325,
		"registers": []
	}`)

	vendor, err := seveneleven.New(logger, vendorConfig, anntStore)
	if err != nil {
		logger.Fatalf("Failed to create vendor: %v", err)
	}
	activeVendor = vendor

	if err := vendor.Start(); err != nil {
		logger.Fatalf("Failed to start vendor: %v", err)
	}

	fmt.Println("  Simulation mode initialized with startup sim ips→ESN mappings")

	testTransactionData := `{"metaData":{"storeNumber":"38551","terminalNumber":"01","transactionSeqNumber":"1234","transactionType":"Sale"},"cartChangeTrail":{"eventType":"startTransaction"}}`

	resp, err := http.Post("http://localhost:6325/register_data", "application/json", bytes.NewBufferString(testTransactionData))
	if err != nil {
		logger.Errorf("Failed to send test data: %v", err)
		return
	}
	resp.Body.Close()

	time.Sleep(500 * time.Millisecond)

	fmt.Println("  Test transaction sent to simulated register")

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	vendor.Stop(ctx)
	apiServer.Stop(ctx)
}

func testProductionMode(logger *log.Logger) {
	os.Setenv("MODE", "production")
	defer os.Unsetenv("MODE")

	dbPath := filepath.Join("data", "test_prod.db")
	defer os.Remove(dbPath)

	// Create ANNT store for production mode test
	anntStore, err := core.NewANNTStore("data/test_annt_production.db", 1, logger)
	if err != nil {
		logger.Fatalf("Failed to create ANNT store: %v", err)
	}
	defer anntStore.Close()

	settingsManager := settings.NewManager(logger)

	var activeVendor vendors.Vendor
	settingsManager.SetUpdateCallback(func(esn string, vendor *settings.VendorConfig) {
		logger.Infof("🔄 Production callback triggered for ESN %s", esn)
		if activeVendor != nil {
			activeVendor.HandleESNConfiguration(esn, vendor)
		}
	})

	vendorGetter := func() api.Vendor {
		if activeVendor != nil {
			return activeVendor
		}
		return nil
	}

	apiServer := api.NewServer(":33481", logger, settingsManager, anntStore, vendorGetter)

	go func() {
		if err := apiServer.Start(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("API Server failed: %v", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	vendorConfig := json.RawMessage(`{
		"brand": "7-eleven",
		"listen_port": 6326,
		"registers": []
	}`)

	vendor, err := seveneleven.New(logger, vendorConfig, anntStore)
	if err != nil {
		logger.Fatalf("Failed to create vendor: %v", err)
	}
	activeVendor = vendor

	if err := vendor.Start(); err != nil {
		logger.Fatalf("Failed to start vendor: %v", err)
	}

	fmt.Println("  Production mode initialized, waiting for camera configurations...")

	mockCameraConfigs := []struct {
		ESN    string
		Config map[string]interface{}
	}{
		{
			ESN: "10037402",
			Config: map[string]interface{}{
				"brand": "7eleven",
				"registers": []map[string]interface{}{
					{
						"store_number":    "38551",
						"terminal_number": "01",
						"ip_address":      "192.168.1.1",
					},
				},
			},
		},
		{
			ESN: "1005c523",
			Config: map[string]interface{}{
				"brand": "7eleven",
				"registers": []map[string]interface{}{
					{
						"store_number":    "38551",
						"terminal_number": "02",
						"ip_address":      "192.168.1.2",
					},
				},
			},
		},
	}

	for _, camera := range mockCameraConfigs {
		fmt.Printf("  Camera %s posting configuration...\n", camera.ESN)

		settingsData := map[string]interface{}{
			"esn":    camera.ESN,
			"vendor": camera.Config,
		}
		postData, _ := json.Marshal(settingsData)

		resp, err := http.Post("http://localhost:33481/pos_config", "application/json", bytes.NewBuffer(postData))
		if err != nil {
			logger.Errorf("Failed to post camera config: %v", err)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			fmt.Printf("    Camera %s configuration accepted\n", camera.ESN)
		} else {
			body, _ := io.ReadAll(resp.Body)
			fmt.Printf("    Camera %s configuration failed: %s\n", camera.ESN, string(body))
		}
		resp.Body.Close()
	}

	time.Sleep(500 * time.Millisecond)

	testTransactionFromIP := func(ip, expectedESN string) {
		fmt.Printf("  Testing transaction from IP %s (expecting ESN %s)...\n", ip, expectedESN)

		testData := `{"metaData":{"storeNumber":"38551","terminalNumber":"01","transactionSeqNumber":"5678","transactionType":"Sale"},"cartChangeTrail":{"eventType":"startTransaction"}}`

		resp, err := http.Post(fmt.Sprintf("http://%s:6326/register_data", ip), "application/json", bytes.NewBufferString(testData))
		if err != nil {
			logger.Errorf("Failed to send transaction data: %v", err)
			return
		}
		resp.Body.Close()
	}

	testTransactionFromIP("192.168.1.1", "10037402")
	testTransactionFromIP("192.168.1.2", "1005c523")

	time.Sleep(500 * time.Millisecond)

	// Test 5: Check if events are available via the API endpoint
	fmt.Print("  Testing events endpoint...")
	resp, err := http.Get("http://localhost:33481/events")
	if err != nil {
		logger.Errorf("Failed to get events: %v", err)
		fmt.Printf("  Failed to connect to events endpoint\n")
	} else {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			fmt.Printf("  Events generated successfully (%d bytes)\n", len(body))
		} else {
			fmt.Printf("  No events available (status: %d)\n", resp.StatusCode)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	vendor.Stop(ctx)
	apiServer.Stop(ctx)

	fmt.Println("  Production mode test completed")
}
