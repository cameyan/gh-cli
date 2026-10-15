package auth

import (
	"errors"

	ghauth "github.com/cli/go-gh/v2/pkg/auth"
)

var ErrUnsupportedHost = errors.New("检测到不支持的主机(host)。请注意，gh attestation 目前不支持 GHES")

func IsHostSupported(host string) error {
	if ghauth.IsEnterprise(host) {
		return ErrUnsupportedHost
	}
	return nil
}
