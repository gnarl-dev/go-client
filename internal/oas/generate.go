// Package oas is generated from the Gnarl OpenAPI description. Do not edit
// oas.gen.go by hand; edit the spec and regenerate.
package oas

// The generator is pinned in tools.mod rather than go.mod. oapi-codegen v2.8.0
// requires a newer Go than this module does, and a `tool` line in go.mod would
// raise the floor for every caller who imports the client just to run a
// generator they never invoke. A separate modfile keeps the pin reproducible
// (CI regenerates with exactly this version) without that cost.
//
//go:generate go tool -modfile=../../tools.mod oapi-codegen -config config.yaml openapi.yaml
