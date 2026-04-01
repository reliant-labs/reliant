// Copyright (c) 2025 Reliant Labs
package models

// DriverMapping maps which models can be used by which drivers
// The key is the driver ID, and the value is the list of models it supports
var DriverMapping = map[Family][]ModelID{} // TODO: Change Family to DriverID in next refactor

// RegisterDriverModels registers which models a driver supports
func RegisterDriverModels(driver Family, models []ModelID) {
	DriverMapping[driver] = append(DriverMapping[driver], models...)
}

// GetModelsForDriver returns all models supported by a given driver
func GetModelsForDriver(driver Family) []ModelID {
	return DriverMapping[driver]
}

// CanDriverUseModel checks if a driver can use a specific model
func CanDriverUseModel(driver Family, modelID ModelID) bool {
	supportedModels, ok := DriverMapping[driver]
	if !ok {
		return false
	}

	for _, id := range supportedModels {
		if id == modelID {
			return true
		}
	}
	return false
}

// GetDriversForModel returns all drivers that can use a specific model
func GetDriversForModel(modelID ModelID) []Family {
	var drivers []Family
	for driver, models := range DriverMapping {
		for _, id := range models {
			if id == modelID {
				drivers = append(drivers, driver)
				break
			}
		}
	}
	return drivers
}
