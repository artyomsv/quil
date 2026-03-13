package pty

// Session represents a pseudo-terminal session wrapping a shell process.
type Session interface {
	// Start launches the given command in a new PTY.
	Start(cmd string, args ...string) error
	// SetEnv sets additional environment variables to merge with os.Environ()
	// when starting the process. Must be called before Start.
	SetEnv(env []string)
	// SetCWD sets the working directory for the spawned process.
	// Must be called before Start.
	SetCWD(dir string)
	// Read reads output from the PTY.
	Read(buf []byte) (int, error)
	// Write sends input to the PTY.
	Write(data []byte) (int, error)
	// Resize changes the PTY window size.
	Resize(rows, cols uint16) error
	// Close terminates the PTY session and cleans up.
	Close() error
	// Pid returns the process ID of the running command.
	Pid() int
}

// NewWithSize creates a new PTY session with the given initial dimensions.
// Falls back to 80x24 if cols or rows are 0.
func NewWithSize(cols, rows int) Session {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	return newWithSize(cols, rows)
}
