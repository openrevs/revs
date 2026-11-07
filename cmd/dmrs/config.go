package main

import (
	"errors"
	"io"
	"strings"

	"github.com/alecthomas/kong"
	"gopkg.in/yaml.v3"
)

// configResolver is a kong.ConfigurationLoader that reads a YAML (or
// JSON) config file. Flags live under a section named for the command
// they configure:
//
//	server:
//	  listen: ":1323"
//	  cut-interval: 30m
//	tui:
//	  data: ~/.dmrs
//
// Keys match flag names; underscores may be used in place of hyphens.
// Kong only consults resolvers for flags not given on the command
// line, so CLI arguments always override config file values.
func configResolver(r io.Reader) (kong.Resolver, error) {
	values := map[string]interface{}{}
	if err := yaml.NewDecoder(r).Decode(&values); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}

	var f kong.ResolverFunc = func(ctx *kong.Context, parent *kong.Path, flag *kong.Flag) (interface{}, error) {
		section := values
		if parent != nil && parent.Node() != nil {
			// Descend into the section for the command that owns this
			// flag (e.g. "server"); app-level flags stay at top level.
			for _, part := range strings.Fields(parent.Node().Path()) {
				sub, ok := section[part].(map[string]interface{})
				if !ok {
					return nil, nil
				}
				section = sub
			}
		}
		if v, ok := section[flag.Name]; ok {
			return v, nil
		}
		if v, ok := section[strings.ReplaceAll(flag.Name, "-", "_")]; ok {
			return v, nil
		}
		return nil, nil
	}
	return f, nil
}
