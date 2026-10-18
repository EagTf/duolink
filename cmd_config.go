package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/eagtf/duolink/internal/config"
)

// configFlags are the command-line overrides shared by `run` and `config`.
// Only flags actually given override the file.
type configFlags struct {
	fs      *flag.FlagSet
	path    *string
	weights *string
	sticky  *int
}

func addConfigFlags(fs *flag.FlagSet) *configFlags {
	return &configFlags{
		fs:      fs,
		path:    fs.String("config", config.DefaultPath(), "config file"),
		weights: fs.String("weights", "", "flow share per uplink, e.g. eth=1,wifi=1 (unnamed uplinks get 0)"),
		sticky:  fs.Int("sticky-minutes", 0, "per-destination stickiness in minutes; 0 turns it off"),
	}
}

// load reads the config file and applies the flags that were set.
func (f *configFlags) load() (config.Config, bool, error) {
	c, found, err := config.Load(*f.path)
	if err != nil {
		return c, found, err
	}
	var ferr error
	f.fs.Visit(func(fl *flag.Flag) {
		switch fl.Name {
		case "weights":
			c.Weights, ferr = config.ParseWeightSpec(*f.weights)
		case "sticky-minutes":
			c.Sticky.Enabled = *f.sticky > 0
			if c.Sticky.Enabled {
				c.Sticky.Minutes = *f.sticky
			}
		}
	})
	if ferr != nil {
		return c, found, ferr
	}
	return c, found, c.Validate()
}

// cmdConfig prints the effective config: defaults, then duolink.json, then flags.
// --init writes the defaults to duolink.json as a starting point.
func cmdConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	cf := addConfigFlags(fs)
	initFile := fs.Bool("init", false, "write the default config to the config file (won't overwrite)")
	fs.Parse(args)

	if *initFile {
		if _, err := os.Stat(*cf.path); err == nil {
			return fmt.Errorf("%s already exists; not overwriting", *cf.path)
		}
		out, _ := json.MarshalIndent(config.Default(), "", "  ")
		if err := os.WriteFile(*cf.path, append(out, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", *cf.path)
		return nil
	}

	c, found, err := cf.load()
	if err != nil {
		return err
	}
	if found {
		fmt.Fprintln(os.Stderr, "# from", *cf.path, "+ defaults + flags")
	} else {
		fmt.Fprintln(os.Stderr, "# no", *cf.path, "- defaults + flags")
	}
	out, _ := json.MarshalIndent(c, "", "  ")
	fmt.Println(string(out))
	return nil
}
