package handlers

import (
	"net"
	"net/http"
)

// scheme is http, or https when the request arrived over TLS.
func scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// origin builds a URL on another port of the same host, keeping the path.
// When port is empty the host is used as it came in.
func origin(scheme, host, port, path string) string {
	if port == "" {
		return scheme + "://" + host + path
	}
	name := host
	if cut, _, err := net.SplitHostPort(host); err == nil {
		name = cut
	}
	return scheme + "://" + net.JoinHostPort(name, port) + path
}

// accountURL is the app account page, seen from a world request. It follows
// the APP_URL setting when a proxy gives the app its own name, and otherwise
// derives the origin from the world's host and APP_PORT.
func (h *Handlers) accountURL(r *http.Request) string {
	if h.cfg.AppURL != "" {
		return h.cfg.AppURL + "/account"
	}
	return origin(scheme(r), r.Host, h.cfg.AppPort, "/account")
}

// playOrigin is the world origin, seen from an app request.
func playOrigin(r *http.Request, cfg Config) string {
	if cfg.PlayURL != "" {
		return cfg.PlayURL
	}
	return origin(scheme(r), r.Host, cfg.PlayPort, "")
}

// appOrigin is the management origin, seen from a world request.
func appOrigin(r *http.Request, cfg Config) string {
	if cfg.AppURL != "" {
		return cfg.AppURL
	}
	return origin(scheme(r), r.Host, cfg.AppPort, "")
}
