package sftp

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/n1lordduck/syncpad/internal/config"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type Client struct {
	ssh  *ssh.Client
	sftp *sftp.Client
	cfg  config.SFTPConfig
}

func Connect(cfg config.SFTPConfig) (*Client, error) {
	authMethods, err := buildAuth(cfg)
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}

	sshCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	conn, err := net.DialTimeout("tcp", addr, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, sshCfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}

	sshClient := ssh.NewClient(sshConn, chans, reqs)
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		_ = sshClient.Close()
		return nil, fmt.Errorf("sftp session: %w", err)
	}

	return &Client{ssh: sshClient, sftp: sftpClient, cfg: cfg}, nil
}

func buildAuth(cfg config.SFTPConfig) ([]ssh.AuthMethod, error) {
	switch cfg.Auth {
	case config.AuthPassword:
		return []ssh.AuthMethod{ssh.Password(cfg.Password)}, nil
	case config.AuthKey:
		keyData, err := os.ReadFile(cfg.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("read key %s: %w", cfg.KeyPath, err)
		}
		signer, err := ssh.ParsePrivateKey(keyData)
		if err != nil {
			return nil, fmt.Errorf("parse key: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}
	return nil, fmt.Errorf("unknown auth type: %s", cfg.Auth)
}

func (c *Client) Upload(localPath, remotePath string) error {
	if err := c.mkdirAll(filepath.Dir(remotePath)); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(remotePath), err)
	}

	src, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open local: %w", err)
	}
	defer func() { _ = src.Close() }()

	dst, err := c.sftp.OpenFile(remotePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("open remote %s: %w", remotePath, err)
	}
	defer func() { _ = dst.Close() }()

	_, err = io.Copy(dst, src)
	return err
}

func (c *Client) Delete(remotePath string) error {
	err := c.sftp.Remove(remotePath)
	if err != nil && !isNotExist(err) {
		return err
	}
	return nil
}

func (c *Client) RemotePath(cfg config.SFTPConfig, localBase, localPath string) string {
	rel, _ := filepath.Rel(localBase, localPath)
	return cfg.RemotePath + "/" + filepath.ToSlash(rel)
}

func (c *Client) mkdirAll(path string) error {
	parts := strings.Split(filepath.ToSlash(path), "/")
	cur := ""
	for _, p := range parts {
		if p == "" {
			cur = "/"
			continue
		}
		cur = cur + p + "/"
		_ = c.sftp.Mkdir(cur)
	}
	return nil
}

func (c *Client) Close() {
	_ = c.sftp.Close()
	_ = c.ssh.Close()
}

func isNotExist(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such file")
}
