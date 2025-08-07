package vendors

import (
	"fmt"
)

var vendorRegistry = make(map[string]NewFunc)

// Register adds a new vendor constructor to the registry.
// This is typically called from the vendor's package init() function.
func Register(name string, newFunc NewFunc) {
	if _, exists := vendorRegistry[name]; exists {
		// Or panic, depending on desired behavior
		return
	}
	vendorRegistry[name] = newFunc
}

// Get returns a new instance of the vendor with the given name.
func Get(name string) (NewFunc, error) {
	newFunc, exists := vendorRegistry[name]
	if !exists {
		return nil, fmt.Errorf("no vendor registered with name: %s", name)
	}
	return newFunc, nil
}
