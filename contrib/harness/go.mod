// contrib/harness — multi-agent stress harness for aq wire formats.
//
// In-process simulation of 20 agents with deterministic chaos. Used to
// empirically compare the codec research lab in contrib/codecs/ under
// adverse conditions.
//
// Like contrib/codecs/, this is a separate Go module so its deps stay
// out of the root aq binary.

module github.com/jwalsh/aq/contrib/harness

go 1.23.3

require github.com/jwalsh/aq/contrib/codecs v0.0.0

require (
	github.com/fxamacker/cbor/v2 v2.7.0 // indirect
	github.com/x448/float16 v0.8.4 // indirect
)

replace github.com/jwalsh/aq/contrib/codecs => ../codecs
