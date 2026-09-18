// Package identity implements identity types and the identity domain.
//
// An identity type declares the payload fields an identity carries (cookies,
// device parameters, tokens, arbitrary JSON, references to vault secrets),
// which of them are sensitive, how identities are deduplicated (unique_by),
// how new identities are activated (probe or immediate) and how a payload is
// delivered to crawler nodes as a Credential with six fixed segments:
// cookies, cookie_header, headers, query, json and values.
//
// The type logic in this package is pure (no database or network I/O):
//
//   - ParseTypeYAML / ParseTypeJSON strictly decode a TypeSpec, apply defaults
//     and validate it; MarshalTypeYAML / MarshalTypeJSON encode it back.
//   - TypeSpec.JSONSchema generates a JSON Schema (draft 2020-12) used to
//     validate imported payloads.
//   - Compile turns a validated spec into an immutable CompiledType that
//     normalizes payloads, computes unique keys, masks sensitive values and
//     renders credentials from pre-parsed delivery templates.
//   - ParseImport reads JSON Lines or CSV identity imports.
//
// Delivery templates only support field placeholders of the form
// "{{ path }}" where path is a field name optionally followed by dotted sub
// keys for cookie_map and json fields ("{{ cookies.sessionid }}"). There are
// no expressions, pipes or function calls, which rules out template
// injection and keeps rendering cheap enough for the Acquire hot path.
//
// Validation failures are reported as *apperr.Error values with reason
// invalid_argument. Error messages never contain payload values.
package identity
