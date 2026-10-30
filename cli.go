package main

import (
	"flag"
	"fmt"
	"io"
)

func runCLI(args []string, stdout, stderr io.Writer, library diagnosticNVML) error {
	flags := flag.NewFlagSet("nvidia-fan-control", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "config.json", "path to the legacy fan-control configuration")
	diagnose := flags.Bool("diagnose", false, "read GPU identity and temperature once, without changing fan settings or reading configuration")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	if *diagnose {
		return runDiagnostic(library, stdout)
	}

	config, err := loadConfig(*configPath)
	if err != nil {
		return fmt.Errorf("load configuration %q: %w", *configPath, err)
	}
	runController(config)
	return nil
}
