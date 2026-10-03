// Package ynf holds what the whole module shares: the published JSON schemas and the version.
package ynf

import _ "embed"

// LanesSchema is docs/schema/lanes.schema.json, the schema for .agents/factory/lanes.yaml.
//
//go:embed docs/schema/lanes.schema.json
var LanesSchema []byte

// ConfigSchema is docs/schema/config.schema.json, the schema for ynf's own config.yaml.
//
//go:embed docs/schema/config.schema.json
var ConfigSchema []byte

// Version is set at build time with -ldflags "-X github.com/eyelock/ynf.Version=...".
var Version = "dev"
