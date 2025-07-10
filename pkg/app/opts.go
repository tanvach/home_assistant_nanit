package app

import (
	"github.com/indiefan/home_assistant_nanit/pkg/mqtt"
	"time"
)

// Opts - application run options
type Opts struct {
	NanitCredentials NanitCredentials
	SessionFile      string
	DataDirectories  DataDirectories
	HTTPEnabled      bool
	MQTT             *mqtt.Opts
	RTMP             *RTMPOpts
	EventPolling     EventPollingOpts
	WebSocketReset   WebSocketResetOpts
}

// NanitCredentials - user credentials for Nanit account
type NanitCredentials struct {
	Email        string
	Password     string
	RefreshToken string
}

// DataDirectories - dictionary of dir paths
type DataDirectories struct {
	BaseDir  string
	VideoDir string
	LogDir   string
}

// RTMPOpts - options for RTMP streaming
type RTMPOpts struct {
	// IP:Port of the interface on which we should listen
	ListenAddr string

	// IP:Port under which can Cam reach the RTMP server
	PublicAddr string
}

type EventPollingOpts struct {
	Enabled         bool
	PollingInterval time.Duration
	MessageTimeout  time.Duration
}

// WebSocketResetOpts - options for WebSocket reset behavior
type WebSocketResetOpts struct {
	// Enable reset behavior when commands fail
	Enabled bool
	
	// Timeout for command responses before marking as failed
	CommandTimeout time.Duration
}
