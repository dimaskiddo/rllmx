package network

import (
	"crypto/tls"
	nethttp "net/http"
	"time"
)

// Defaults for a client that names none.
const (
	defaultConnectTimeout = 60 * time.Second
	defaultMaxRedirects   = 5
	defaultBodyCap        = 1 << 20
)

// Options configure one guarded client. Zero values take the defaults above,
// except MaxRedirects where zero means "the default of 5" and a negative value
// means "follow none".
type Options struct {
	ConnectTimeout time.Duration
	MaxRedirects   int
	BodyCap        int64
	Allow          *Allow
	// TLS overrides the TLS settings, for a caller that pins a certificate or a
	// server name (the local health probe does).
	TLS *tls.Config
	// DisableKeepAlives leaves no idle connection behind, for a one-shot check.
	DisableKeepAlives bool
	// CipherSuites pins the TLS 1.2 suites a call offers, for a gateway that
	// reads the offer itself. Empty takes the standard list; TLS 1.3 suites are
	// not configurable and are always offered.
	CipherSuites []uint16
	// Curves pins the key-exchange group a call offers. Empty takes the standard
	// list; one entry offers only that group, which changes the ClientHello
	// enough for a network that resets the default one.
	Curves []tls.CurveID
}

func (o Options) connectTimeout() time.Duration {
	if o.ConnectTimeout > 0 {
		return o.ConnectTimeout
	}
	return defaultConnectTimeout
}

func (o Options) redirects() int {
	if o.MaxRedirects == 0 {
		return defaultMaxRedirects
	}
	return o.MaxRedirects
}

// BodyLimit is the byte cap a caller reads a response under. Negative means no
// cap, zero means one mebibyte.
func (o Options) BodyLimit() int64 {
	if o.BodyCap == 0 {
		return defaultBodyCap
	}
	return o.BodyCap
}

// NewTransport builds the one transport every outbound call shares. It reads no
// proxy variable, verifies TLS and routes every dial through the guard.
func NewTransport(o Options) *nethttp.Transport {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if o.TLS != nil {
		tlsConfig = o.TLS.Clone()
		if tlsConfig.MinVersion < tls.VersionTLS12 {
			tlsConfig.MinVersion = tls.VersionTLS12
		}
	}
	if len(o.CipherSuites) > 0 {
		tlsConfig.CipherSuites = o.CipherSuites
	}
	if len(o.Curves) > 0 {
		tlsConfig.CurvePreferences = o.Curves
	}
	guard := NewDialer(o.connectTimeout(), o.Allow)
	return &nethttp.Transport{
		Proxy:                 nil,
		DialContext:           guard.DialContext,
		TLSClientConfig:       tlsConfig,
		TLSHandshakeTimeout:   o.connectTimeout(),
		ResponseHeaderTimeout: o.connectTimeout(),
		ForceAttemptHTTP2:     true,
		DisableKeepAlives:     o.DisableKeepAlives,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		// Long on purpose: a dropped idle socket costs one immediate replay
		// (executor.Default), and Go keeps no TLS session here, so a short pool
		// would pay a full handshake after every pause.
		IdleConnTimeout: 90 * time.Second,
	}
}

// NewClient builds a guarded client. A response body is left for the caller to
// cap with Limit, because only the caller knows whether the answer is small.
func NewClient(o Options) *nethttp.Client {
	max := o.redirects()
	return &nethttp.Client{
		Transport: NewTransport(o),
		CheckRedirect: func(req *nethttp.Request, via []*nethttp.Request) error {
			// via holds the requests already made, so a limit of 3 refuses the
			// fourth redirect.
			if len(via) > max {
				return nethttp.ErrUseLastResponse
			}
			// Never follow a downgrade: a redirect from TLS to plain HTTP would
			// hand the next hop everything it needs to read the request.
			if len(via) > 0 && req.URL.Scheme != "https" && via[len(via)-1].URL.Scheme == "https" {
				return nethttp.ErrUseLastResponse
			}
			return nil
		},
	}
}
