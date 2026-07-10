// Package exec discovers and runs the whisper-cli and ffmpeg binaries.
// Runners are interface-based so a future whisper-server backend can be
// swapped in. Child processes are always started with an argument slice;
// invoking a shell (sh -c) is forbidden.
//
// Layer rule: exec is the lowest layer; it must not reference HTTP concepts.
package exec
