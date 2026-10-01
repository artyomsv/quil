//go:build unix && !linux

package ipc

import "net"

// listenUnixPrivate does NOT narrow the umask outside Linux. umask is
// process-wide and inherited across fork, so changing it is only safe while
// forks are blocked by holding syscall.ForkLock for writing — and on these
// platforms net itself takes ForkLock for reading to create the socket, so
// holding it would deadlock the bind. The socket is protected instead by
// the chmod 0600 that protectSocket applies immediately after Listen, and,
// for the moment between the two, by the 0700 QUIL_HOME directory around
// it, which no other account can traverse.
func listenUnixPrivate(path string) (net.Listener, error) { return net.Listen("unix", path) }
