package system

// IDMap is one kernel user namespace mapping, with an exclusive upper bound
// of Inside+Length and Outside+Length respectively.
type IDMap struct {
	Inside  uint64
	Outside uint64
	Length  uint64
}

// UserNamespace identifies the actual daemon behind a local Docker socket.
// Device and Inode pin its namespace identity when it is reopened for use.
type UserNamespace struct {
	PID     int
	PeerUID int
	PeerGID int
	Device  uint64
	Inode   uint64
	UIDMap  []IDMap
	GIDMap  []IDMap
}
