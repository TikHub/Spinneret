// Package vault implements Spinneret's envelope encryption: key-encryption
// keys (KEKs), per-record data-encryption keys (DEKs), a DEK cache, and the
// domain services built on top of them (secrets, KEK rewrap job).
//
// # Envelope format
//
// Every encrypted value is sealed with a fresh random 32-byte DEK:
//
//	ciphertext  = nonce(12) || AES-256-GCM(DEK, nonce, plaintext, AAD)
//	wrapped DEK = nonce(12) || AES-256-GCM(KEK, nonce, DEK, "spinneret-dek:"+kekID)
//
// The data AAD binds a ciphertext to the record and field it belongs to (see
// [AAD]), so a ciphertext copied into another row or column fails to decrypt.
// The DEK AAD binds a wrapped DEK to the id of the KEK that wrapped it, so a
// wrapped DEK relabelled with another KEK id fails to unwrap even when two ids
// share the same key material.
//
// # KEK configuration
//
// [LoadLocalKEKProvider] reads keys from SPINNERET_KEK_FILE and SPINNERET_KEKS
// (see the function documentation for the accepted formats). Key material is
// never included in error messages or logs.
//
// # Caching
//
// [Cipher.Open] caches the AES-GCM instance derived from each unwrapped DEK in
// an LRU cache with a TTL, keyed by SHA-256 over the KEK id and the wrapped
// DEK, so repeated reads of hot records skip the KEK unwrap. Raw DEK bytes are
// zeroed as soon as the AES key schedule has been derived.
package vault
