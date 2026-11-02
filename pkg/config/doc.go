// Package config resolves a project's typed configuration from proto field
// annotations at runtime, with no per-field generated loader.
//
// The config object IS the proto message: a project holds a
// *configv1.AppConfig and calls [LoadInto] / [RegisterFlags], which read the
// (forge.v1.config) options off the message descriptor and resolve every
// field through the canonical forge precedence — proto default < config file
// < environment variable < explicit CLI flag.
//
// Three layers live here:
//
//   - loader.go     the descriptor walk and the precedence resolution.
//   - filelayer.go  the first-class config-FILE layer (--config, or the
//     binary's <BINARY>_CONFIG), which loads only for an
//     explicitly-given path and fails loudly on a missing or
//     invalid one.
//   - semantic.go   the role-annotation helpers ([Mode], [Validate]) and the
//     cobra-facing surface.
//
// The defining property of the semantic helpers: none of them match on a
// field's NAME. [Mode] keys off the field tagged role=CONFIG_FIELD_ROLE_MODE,
// [Validate]'s TLS and CORS invariants key off their own role tags. Renaming
// an annotated field changes nothing; naming an UNannotated field
// "environment" grants it no behavior. Behavior follows the annotation, so a
// rename can never silently drop a guard.
package config
