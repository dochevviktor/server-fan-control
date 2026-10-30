package main

import (
	"encoding/json"
	"errors"
	"flag"
	"log"
	"os"
	"time"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

// Config structure to match the provided JSON
type Config struct {
	TimeToUpdate      int                `json:"time_to_update"`
	TemperatureRanges []TemperatureRange `json:"temperature_ranges"`
}

type TemperatureRange struct {
	MinTemperature int `json:"min_temperature"`
	MaxTemperature int `json:"max_temperature"`
	FanSpeed       int `json:"fan_speed"`
	Hysteresis     int `json:"hysteresis"`
}

// LoadConfig reads the JSON config file
func loadConfig(file string) (Config, error) {
	var config Config
	data, err := os.ReadFile(file) // Updated to use os.ReadFile
	if err != nil {
		return config, err
	}
	err = json.Unmarshal(data, &config)
	return config, err
}

// Absolute difference function
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// GetFanSpeedForTemperature determines the appropriate fan speed based on temperature and hysteresis
func getFanSpeedForTemperature(temp, prevTemp, prevSpeed int, ranges []TemperatureRange) int {
	for _, r := range ranges {
		if temp > r.MinTemperature && temp <= r.MaxTemperature {
			// Apply hysteresis: Change fan speed only if temperature has moved significantly
			if abs(temp-prevTemp) >= r.Hysteresis || prevSpeed != r.FanSpeed {
				return r.FanSpeed
			}
		}
	}
	// Default to the previous fan speed if no range matches
	return prevSpeed
}

func main() {
	// Set logging to stdout
	log.SetOutput(os.Stdout)

	if err := runCLI(os.Args[1:], os.Stdout, os.Stderr, nvml.New()); err != nil && !errors.Is(err, flag.ErrHelp) {
		log.SetOutput(os.Stderr)
		log.Print(err)
		os.Exit(1)
	}
}

// The legacy controller is retained for native deployments. Its validation,
// fan restoration and shutdown behavior will be hardened in phase 2.
func runController(config Config) {
	// Initialize NVML
	ret := nvml.Init()
	if ret != nvml.SUCCESS {
		log.Fatalf("Unable to initialize NVML: %v", nvml.ErrorString(ret))
	}
	defer func() {
		ret := nvml.Shutdown()
		if ret != nvml.SUCCESS {
			log.Fatalf("Unable to shutdown NVML: %v", nvml.ErrorString(ret))
		}
	}()

	// Get GPU count
	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		log.Fatalf("Unable to get device count: %v", nvml.ErrorString(ret))
	}

	// Initialize tracking variables
	prevTemps := make([]int, count)     // Previous temperatures for each GPU
	prevFanSpeeds := make([]int, count) // Previous fan speeds for each GPU

	// Monitoring loop
	for {
		for i := 0; i < count; i++ {
			device, ret := nvml.DeviceGetHandleByIndex(i)
			if ret != nvml.SUCCESS {
				log.Printf("Unable to get device at index %d: %v", i, nvml.ErrorString(ret))
				continue
			}

			// Get current temperature
			temp, ret := nvml.DeviceGetTemperature(device, nvml.TEMPERATURE_GPU)
			if ret != nvml.SUCCESS {
				log.Printf("Unable to get temperature for GPU %d: %v", i, nvml.ErrorString(ret))
				continue
			}

			// Convert uint32 temperature to int for compatibility
			tempInt := int(temp)

			// Determine appropriate fan speed
			newFanSpeed := getFanSpeedForTemperature(tempInt, prevTemps[i], prevFanSpeeds[i], config.TemperatureRanges)

			// Update fan speed only if it has changed
			if newFanSpeed != prevFanSpeeds[i] {
				// Set manual fan control policy
				ret = nvml.DeviceSetFanControlPolicy(device, 0, 1)
				if ret != nvml.SUCCESS {
					log.Printf("Unable to set manual fan control policy for GPU %d: %v", i, nvml.ErrorString(ret))
					continue
				}

				// Set the new fan speed
				ret = nvml.DeviceSetFanSpeed_v2(device, 0, newFanSpeed)
				if ret != nvml.SUCCESS {
					log.Printf("Unable to set fan speed for GPU %d: %v", i, nvml.ErrorString(ret))
					continue
				}

				// Log the change
				log.Printf("Updated GPU %d: Temp=%d°C, Fan Speed=%d%%", i, tempInt, newFanSpeed)

				// Update tracking variables
				prevFanSpeeds[i] = newFanSpeed
			}

			// Update the previous temperature
			prevTemps[i] = tempInt
		}

		// Wait for the configured update interval
		time.Sleep(time.Duration(config.TimeToUpdate) * time.Second)
	}
}
