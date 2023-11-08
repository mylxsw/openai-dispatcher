package internal

import (
	"golang.org/x/net/proxy"
)

func createSocks5Dialer(proxyAddr string) (proxy.Dialer, error) {
	dialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, proxy.Direct)
	if err != nil {
		return nil, err
	}

	return dialer, nil
}
