//go:build !linux && !darwin && !windows

package proctree

// Platforms with no enumeration source. The dialog renders the section as
// unsupported rather than empty — an empty tree and an unsupported platform are
// different claims, and only one of them is true here.

const tableHasStarts = true

const cpuIsSampled = false

func readTable() ([]ProcessEntry, error) { return nil, ErrUnsupported }

func readCPU(_ []int) cpuReading { return cpuReading{Supported: false} }

func enrichStarts(table []ProcessEntry, _ []int) []ProcessEntry { return table }
