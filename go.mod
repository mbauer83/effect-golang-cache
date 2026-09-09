module github.com/mbauer83/effect-golang-cache

go 1.27.0

require (
	// Test-only: a Redis that speaks the protocol and runs the Lua, so what
	// the scripts here actually do is read off a server rather than asserted.
	// It also lets the clock be moved, which a rate measured in seconds
	// otherwise makes a test wait for.
	github.com/alicebob/miniredis/v2 v2.39.0
	github.com/mbauer83/effect-golang v0.2.0
	github.com/redis/go-redis/v9 v9.22.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/sys v0.30.0 // indirect
)

// Every module of effect-golang is versioned together and released in
// dependency order, so a version here is a version that exists. While several
// are being worked on at once, the go.work above this directory resolves them
// to the working copies beside each other.
