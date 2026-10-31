package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func flagEnvironmentName(name string) string {
	return "FAN_CONTROL_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// Explicit flags take precedence, including an explicit false or empty string.
// Empty environment values leave the native defaults in place.
func applyFlagEnvironment(f *flag.FlagSet, names ...string) error {
	explicit := make(map[string]bool)
	f.Visit(func(option *flag.Flag) { explicit[option.Name] = true })
	for _, name := range names {
		if explicit[name] {
			continue
		}
		env := flagEnvironmentName(name)
		value := os.Getenv(env)
		if value == "" {
			continue
		}
		if err := f.Set(name, value); err != nil {
			// All fallible values are booleans. Avoid echoing environment contents.
			return fmt.Errorf("%s must be a boolean (true or false)", env)
		}
	}
	return nil
}
