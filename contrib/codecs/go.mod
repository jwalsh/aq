// contrib/codecs — speculative wire format research for aq.
//
// This module is intentionally separate from the root aq module so that
// experimental dependencies (CBOR, fuzzing libraries, etc.) do not leak
// into the core aq binary, which remains stdlib-only.
//
// Codecs implemented here are research prototypes for the multi-codec
// stress harness. None are canonical. The "best" codec is determined
// empirically by the harness, not by which one is listed here.

module github.com/jwalsh/aq/contrib/codecs

go 1.23.3

require github.com/fxamacker/cbor/v2 v2.7.0

require github.com/x448/float16 v0.8.4 // indirect
