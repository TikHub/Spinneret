// Package policy implements the four Spinneret policy kinds (rotation,
// signal, action and breaker): their YAML/JSON specifications, strict
// decoding, defaults, validation, built-in defaults, binding resolution,
// extends chains, the compiled signal classifier and the compiled action
// rule evaluator.
//
// The package is pure: it performs no I/O besides reading its embedded
// default policy files, so it can be used from the API layer, the catalog,
// the worker and tests alike.
//
// Decoding semantics: ParseYAML and ParseJSON decode on top of a spec that
// already carries every default, so an explicitly written zero value (for
// example a breaker trip threshold of 0, which disables that condition, or
// enabled: false) is preserved. ApplyDefaults then fills the remaining zero
// values whose zero is not a legal setting (for example lease_ttl or a cooldown
// rule multiplier), which also covers list elements such as action rules.
package policy
