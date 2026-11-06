package update

import (
	"errors"
	"net"
	"net/url"
	"strconv"
)

func urlForHealth(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/healthy" {
		return "", errors.New("invalid local update health URL")
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || host != "127.0.0.1" {
		return "", errors.New("update health endpoint must be IPv4 loopback")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", errors.New("invalid update health port")
	}
	return u.String(), nil
}
