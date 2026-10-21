package sshx

import (
	"golang.org/x/crypto/ssh"

	"nlrshell/internal/store"
)

// Conn is a bare SSH connection with no terminal or monitor, for callers
// that only run commands and use SFTP (such as the MCP server).
type Conn struct {
	*ssh.Client
	chain []*ssh.Client
}

// Dial connects to p, going through its jump hosts, without opening a
// shell. Secrets come from the profile first and from prompter after that.
func Dial(p store.Profile, st *store.Store, prompter Prompter) (*Conn, error) {
	d := &dialer{st: st, prompter: prompter, status: func(string) {}}
	chain, err := d.dial(p, 0)
	if err != nil {
		return nil, err
	}
	return &Conn{Client: chain[0], chain: chain}, nil
}

// Close closes the connection and its jump host connections.
func (c *Conn) Close() error {
	closeClients(c.chain)
	return nil
}
