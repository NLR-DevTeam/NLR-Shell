package sshx

import (
	"errors"
	"net"
	"os"
	"time"
)

// dialAgent connects to the OpenSSH agent. On Windows it is a named pipe
// served by the ssh-agent service.
func dialAgent() (net.Conn, error) {
	conn, err := os.OpenFile(`\\.\pipe\openssh-ssh-agent`, os.O_RDWR, 0)
	if err != nil {
		return nil, errors.New("无法连接 OpenSSH Agent（请确认 ssh-agent 服务已启动）")
	}
	return pipeConn{conn}, nil
}

// pipeConn adapts the agent pipe so it is closed together with the dialer.
type pipeConn struct{ *os.File }

func (pipeConn) LocalAddr() net.Addr              { return nil }
func (pipeConn) RemoteAddr() net.Addr             { return nil }
func (pipeConn) SetDeadline(time.Time) error      { return nil }
func (pipeConn) SetReadDeadline(time.Time) error  { return nil }
func (pipeConn) SetWriteDeadline(time.Time) error { return nil }
