//go:build !windows

package sshx

import (
	"errors"
	"net"
	"os"
)

// dialAgent connects to the OpenSSH agent through the Unix socket named by
// SSH_AUTH_SOCK.
func dialAgent() (net.Conn, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("无法连接 SSH Agent（未设置 SSH_AUTH_SOCK，请先启动 ssh-agent）")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, errors.New("无法连接 SSH Agent（" + err.Error() + "）")
	}
	return conn, nil
}
