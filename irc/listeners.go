// Copyright (c) 2020 Shivaram Lingamneni <slingamn@cs.stanford.edu>
// released under the MIT license

package irc

import (
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/oragono/oragono/irc/utils"
)

var (
	errCantReloadListener = errors.New("can't switch a listener between stream and websocket")
)

// IRCListener is an abstract wrapper for a listener (TCP port or unix domain socket).
// Server tracks these by listen address and can reload or stop them during rehash.
type IRCListener interface {
	Reload(config utils.ListenerConfig) error
	Stop() error
}

// NewListener creates a new listener according to the specifications in the config file
func NewListener(server *Server, addr string, config utils.ListenerConfig, bindMode os.FileMode) (result IRCListener, err error) {
	baseListener, err := createBaseListener(addr, bindMode)
	if err != nil {
		return
	}

	wrappedListener := utils.NewReloadableListener(baseListener, config)

	if config.WebSocket {
		return NewWSListener(server, addr, wrappedListener, config)
	} else {
		return NewNetListener(server, addr, wrappedListener, config)
	}
}

func createBaseListener(addr string, bindMode os.FileMode) (listener net.Listener, err error) {
	addr = strings.TrimPrefix(addr, "unix:")
	if strings.HasPrefix(addr, "/") {
		// https://stackoverflow.com/a/34881585
		os.Remove(addr)
		listener, err = net.Listen("unix", addr)
		if err == nil && bindMode != 0 {
			os.Chmod(addr, bindMode)
		}
	} else {
		listener, err = net.Listen("tcp", addr)
	}
	return
}

// NetListener is an IRCListener for a regular stream socket (TCP or unix domain)
type NetListener struct {
	listener *utils.ReloadableListener
	server   *Server
	addr     string
}

func NewNetListener(server *Server, addr string, listener *utils.ReloadableListener, config utils.ListenerConfig) (result *NetListener, err error) {
	nl := NetListener{
		server:   server,
		listener: listener,
		addr:     addr,
	}
	go nl.serve()
	return &nl, nil
}

func (nl *NetListener) Reload(config utils.ListenerConfig) error {
	if config.WebSocket {
		return errCantReloadListener
	}
	nl.listener.Reload(config)
	return nil
}

func (nl *NetListener) Stop() error {
	return nl.listener.Close()
}

// ensure that any IP we got from the PROXY line is trustworthy (otherwise, clear it)
func validateProxiedIP(conn *utils.WrappedConn, config *Config) {
	if !utils.IPInNets(utils.AddrToIP(conn.RemoteAddr()), config.Server.proxyAllowedFromNets) {
		conn.ProxiedIP = nil
	}
}

func (nl *NetListener) serve() {
	for {
		conn, err := nl.listener.Accept()

		if err == nil {
			// hand off the connection
			wConn, ok := conn.(*utils.WrappedConn)
			if ok {
				if wConn.ProxiedIP != nil {
					validateProxiedIP(wConn, nl.server.Config())
				}
				go nl.server.RunClient(NewIRCStreamConn(wConn))
			} else {
				nl.server.logger.Error("internal", "invalid connection type", nl.addr)
			}
		} else if err == utils.ErrNetClosing {
			return
		} else {
			nl.server.logger.Error("internal", "accept error", nl.addr, err.Error())
		}
	}
}

// WSListener is a listener for IRC-over-websockets (initially HTTP, then upgraded to a
// different application protocol that provides a message-based API, possibly with TLS)
type WSListener struct {
	sync.Mutex // tier 1
	listener   *utils.ReloadableListener
	httpServer *http.Server
	server     *Server
	addr       string
	config     utils.ListenerConfig
}

func NewWSListener(server *Server, addr string, listener *utils.ReloadableListener, config utils.ListenerConfig) (result *WSListener, err error) {
	result = &WSListener{
		listener: listener,
		server:   server,
		addr:     addr,
		config:   config,
	}
	result.httpServer = &http.Server{
		Handler:      http.HandlerFunc(result.handle),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	go result.httpServer.Serve(listener)
	return
}

func (wl *WSListener) Reload(config utils.ListenerConfig) error {
	if !config.WebSocket {
		return errCantReloadListener
	}
	wl.listener.Reload(config)
	return nil
}

func (wl *WSListener) Stop() error {
	return wl.httpServer.Close()
}

func (wl *WSListener) handle(w http.ResponseWriter, r *http.Request) {
	config := wl.server.Config()
	proxyAllowedFrom := config.Server.proxyAllowedFromNets
	proxiedIP := utils.HandleXForwardedFor(r.RemoteAddr, r.Header.Get("X-Forwarded-For"), proxyAllowedFrom)

	wsUpgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			if len(config.Server.WebSockets.allowedOriginRegexps) == 0 {
				return true
			}
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			if len(origin) == 0 {
				return false
			}
			for _, re := range config.Server.WebSockets.allowedOriginRegexps {
				if re.MatchString(origin) {
					return true
				}
			}
			return false
		},
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		wl.server.logger.Info("internal", "websocket upgrade error", wl.addr, err.Error())
		return
	}

	wConn, ok := conn.UnderlyingConn().(*utils.WrappedConn)
	if !ok {
		wl.server.logger.Error("internal", "non-proxied connection on websocket", wl.addr)
		conn.Close()
		return
	}
	if wConn.ProxiedIP != nil {
		validateProxiedIP(wConn, config)
	} else {
		// if there was no PROXY protocol IP, use the validated X-Forwarded-For IP instead,
		// unless it is redundant
		if proxiedIP != nil && !proxiedIP.Equal(utils.AddrToIP(wConn.RemoteAddr())) {
			wConn.ProxiedIP = proxiedIP
		}
	}

	// avoid a DoS attack from buffering excessively large messages:
	conn.SetReadLimit(maxReadQBytes)

	go wl.server.RunClient(NewIRCWSConn(conn))
}