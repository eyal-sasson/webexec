// Package server holds the code that runs a webrtc based service
// connecting commands with datachannels thru a pseudo tty.
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/afittestide/webexec/signal"
	"github.com/creack/pty"
	"github.com/pion/webrtc/v2"
)

const connectionTimeout = 600 * time.Second
const keepAliveInterval = 3 * time.Second

// type WebRTCServer is the singelton we use to store server globals
type WebRTCServer struct {
	c  webrtc.Configuration
	pc *webrtc.PeerConnection
	// channels holds all the open channel we have with process ID as key
	channels map[int]*TerminalChannel
}

func NewWebRTCServer() (server WebRTCServer, err error) {
	// Create a new API with a custom logger
	// This SettingEngine allows non-standard WebRTC behavior
	s := webrtc.SettingEngine{}
	s.SetConnectionTimeout(connectionTimeout, keepAliveInterval)
	api := webrtc.NewAPI(webrtc.WithSettingEngine(s))

	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	}
	server = WebRTCServer{c: config,
		channels: make(map[int]*TerminalChannel)}
	//TODO: call func (e *SettingEngine) SetEphemeralUDPPortRange(portMin, portMax uint16)
	pc, err := api.NewPeerConnection(config)
	if err != nil {
		err = fmt.Errorf("Failed to open peer connection: %q", err)
		return
	}
	server.pc = pc
	// Set the handler for ICE connection state
	// This will notify you when the peer has connected/disconnected
	pc.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		s := connectionState.String()
		log.Printf("ICE Connection State change: %s\n", s)
		if s == "connected" {
			// TODO add initialization code
		}
	})
	// Register data channel creation handling
	pc.OnDataChannel(func(d *webrtc.DataChannel) {
		var cmd *exec.Cmd
		if d.Label() == "signaling" {
			return
		}
		d.OnOpen(func() {
			var p *os.File
			l := d.Label()
			log.Printf("New Data channel %q\n", l)
			c := strings.Split(l, " ")
			if err != nil {
				log.Panicf("Failed to attach a ptyi and start cmd: %v", err)
			}
			defer func() { _ = p.Close() }() // Best effort.
			// We get "terminal7" in c[0] as the first channel name
			// from a fresh client. This dc is used for as ctrl channel
			if c[0] == "%" {
				d.OnMessage(server.OnCTRLMsg)
				return
			}
			var firstRune rune = rune(c[0][0])
			// If the message starts with a digit we assume it starts with
			// a size
			if unicode.IsDigit(firstRune) {
				ws, err := server.ParseWinsize(c[0])
				if err != nil {
					log.Printf("Failed to parse winsize: %q ", c[0])
				}
				cmd = exec.Command(c[1], c[2:]...)
				log.Printf("starting command with size: %v", ws)
				p, err = pty.StartWithSize(cmd, &ws)
			} else {
				cmd = exec.Command(c[0], c[1:]...)
				log.Printf("starting command without size")
				p, err = pty.Start(cmd)
			}
			if err != nil {
				log.Panicf("Failed to start pty: %v", err)
			}
			// create the channel and add to the server's channels map
			channelId := cmd.Process.Pid
			channel := TerminalChannel{d, cmd, p}
			server.channels[channelId] = &channel
			d.OnMessage(channel.OnMessage)
			// send the channel id as the first message
			s := strconv.Itoa(channelId)
			bs := []byte(s)
			d.Send(bs)
			// use copy to read command output and send it to the data channel
			_, err = io.Copy(&channel, p)
			if err != nil {

				log.Printf("Process state: %v", cmd.ProcessState)
				log.Printf("Failed to copy from command: %v %v", err,
					cmd.ProcessState.String())
			}
			p.Close()
			err = cmd.Process.Kill()
			if err != nil {
				log.Printf("Failed to kill process: %v %v",
					err, cmd.ProcessState.String())
			}
			d.Close()
		})
		d.OnClose(func() {
			err = cmd.Process.Kill()
			if err != nil {
				log.Printf("Failed to kill process: %v %v", err, cmd.ProcessState.String())
			}
			log.Println("Data channel closed")
		})
	})
	return
}

// Listen
func (server *WebRTCServer) Listen(remote string) []byte {
	offer := webrtc.SessionDescription{}
	signal.Decode(remote, &offer)
	err := server.pc.SetRemoteDescription(offer)
	if err != nil {
		panic(err)
	}
	answer, err := server.pc.CreateAnswer(nil)
	if err != nil {
		panic(err)
	}

	// Sets the LocalDescription, and starts our UDP listeners
	err = server.pc.SetLocalDescription(answer)
	if err != nil {
		panic(err)
	}
	return []byte(signal.Encode(answer))
}

// Shutdown is called when it's time to go.
// Sweet dreams.
func (server *WebRTCServer) Shutdown() {
	server.pc.Close()
	for _, channel := range server.channels {
		if channel.cmd != nil {
			log.Print("Shutting down WebRTC server")
			channel.cmd.Process.Kill()
		}
	}
}

// OnCTRLMsg handles incoming control messages
func (server *WebRTCServer) OnCTRLMsg(msg webrtc.DataChannelMessage) {
	var m CTRLMessage
	// fmt.Printf("Got a terminal7 message: %q", string(msg.Data))
	p := json.Unmarshal(msg.Data, &m)
	if m.resizePTY != nil {
		var ws pty.Winsize
		ws.Cols = m.resizePTY.sx
		ws.Rows = m.resizePTY.sy
		pty.Setsize(server.channels[m.resizePTY.id].pty, &ws)
	}
	// TODO: add more commands here: mouse, clipboard, etc.
	log.Printf("< %v", p)
}

// ErrorArgs is a type that holds the args for an error message
type ErrorArgs struct {
	Description string
	// Ref holds the message id the error refers to or 0 for system errors
	Ref uint32
}

// ResizePTYArgs is a type that holds the argumnet to the resize pty command
type ResizePTYArgs struct {
	id int
	sx uint16
	sy uint16
}

// CTRLMessage type holds control messages passed over the control channel
type CTRLMessage struct {
	time      float64
	resizePTY *ResizePTYArgs `json:"resize_pty"`
	Error     *ErrorArgs
}

// Type TerminalChannel holds the holy trinity: a data channel, a command and
// a pseudo tty.
type TerminalChannel struct {
	dc  *webrtc.DataChannel
	cmd *exec.Cmd
	pty *os.File
}

// Write send a buffer of data over the data channel
// TODO: rename this function, we use Write because of io.Copy
func (channel *TerminalChannel) Write(p []byte) (int, error) {
	// TODO: logging...
	if true {
		text := string(p)
		for _, r := range strings.Split(text, "\r\n") {
			if len(r) > 0 {
				log.Printf("> %q\n", r)
			}
		}
	}
	err := channel.dc.Send(p)
	if err != nil {
		return 0, fmt.Errorf("Data channel send failed: %v", err)
	}
	//TODO: can we get a truer value than `len(p)`
	return len(p), nil
}

// OnMessage is called on incoming messages from the data channel.
// It simply write the recieved data to the pseudo tty
func (channel *TerminalChannel) OnMessage(msg webrtc.DataChannelMessage) {
	p := msg.Data
	log.Printf("< %v", p)
	l, err := channel.pty.Write(p)
	if err != nil {
		log.Panicf("Stdin Write returned an error: %v", err)
	}
	if l != len(p) {
		log.Panicf("stdin write wrote %d instead of %d bytes", l, len(p))
	}
}

// ParseWinsize gets a string in the format of "24x80" and returns a Winsize
func (server *WebRTCServer) ParseWinsize(s string) (ws pty.Winsize, err error) {
	dim := strings.Split(s, "x")
	sx, err := strconv.ParseInt(dim[1], 0, 16)
	ws = pty.Winsize{0, 0, 0, 0}
	if err != nil {
		return ws, fmt.Errorf("Failed to parse number of cols: %v", err)
	}
	sy, err := strconv.ParseInt(dim[0], 0, 16)
	if err != nil {
		return ws, fmt.Errorf("Failed to parse number of rows: %v", err)
	}
	ws = pty.Winsize{uint16(sy), uint16(sx), 0, 0}
	return
}
