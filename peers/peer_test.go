// This files contains testing suites that test webexec as a compoennt and
// using a test client
package peers

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/stretchr/testify/require"
)

func TestSimpleEcho(t *testing.T) {
	initTest(t)
	closed := make(chan bool)
	client, cert, err := NewClient(true)
	peer, err := NewPeer(cert)
	require.Nil(t, err, "NewPeer failed with: %s", err)
	// count the incoming messages
	count := 0
	dc, err := client.CreateDataChannel("echo,hello world", nil)
	require.Nil(t, err, "Failed to create the echo data channel: %v", err)
	dc.OnOpen(func() {
		Logger.Infof("Channel %q opened", dc.Label())
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		// first get a channel Id and then "hello world" text
		Logger.Infof("got message: #%d %q", count, string(msg.Data))
		if count == 0 {
			_, err = strconv.Atoi(string(msg.Data))
			require.Nil(t, err, "Failed to cover channel it to int: %v", err)
		} else if count == 1 {
			require.EqualValues(t, string(msg.Data)[:11], "hello world",
				"Got bad msg: %q", msg.Data)
		}
		count++
	})
	dc.OnClose(func() {
		Logger.Info("Client Data channel closed")
		closed <- true
	})
	SignalPair(client, peer)
	// TODO: add timeout
	<-closed
	panes := Panes.All()
	lp := panes[len(panes)-1]

	waitForChild(lp.C.Process.Pid, time.Second)
	require.False(t, lp.IsRunning)
	// For some reason we sometimes get an empty message and count can be 3
	require.GreaterOrEqual(t, count, 2, "Expected to recieve 2 messages and got %d", count)
}

func TestResizeCommand(t *testing.T) {
	initTest(t)
	done := make(chan bool)
	client, cert, err := NewClient(true)
	require.Nil(t, err, "Failed to create a new client %v", err)
	peer, err := NewPeer(cert)
	require.Nil(t, err, "NewPeer failed with: %s", err)
	cdc, err := client.CreateDataChannel("%", nil)
	require.Nil(t, err, "failed to create the control data channel: %v", err)
	cdc.OnOpen(func() {
		cdc.OnMessage(func(msg webrtc.DataChannelMessage) {
			ack := ParseAck(t, msg)
			if ack.Ref == 456 {
				done <- true
			}
		})
		// control channel is open let's open another one, so we'll have
		// what to resize
		dc, err := client.CreateDataChannel("12x34,bash", nil)
		require.Nil(t, err, "failed to create the a channel: %v", err)
		// paneID hold the ID of the channel as recieved from the server
		paneID := -1
		dc.OnOpen(func() {
			Logger.Info("Data channel is open")
			// send something to get the channel going
			// dc.Send([]byte{'#'})
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				var rows int
				var cols int
				Logger.Infof("Got data channel message: %q", string(msg.Data))
				if paneID == -1 {
					_, err := fmt.Sscanf(
						string(msg.Data), "%d,%dx%d", &paneID, &rows, &cols)
					require.Nil(t, err,
						"Failed to parse first message: %q", string(msg.Data))
					require.Equal(t, 12, rows, "Got aa bad number of rows")
					require.Equal(t, 34, cols, "Got aa bad number of cols")
					resizeArgs := ResizeArgs{paneID, 80, 24}
					m := CTRLMessage{time.Now().UnixNano(), 456, "resize",
						&resizeArgs}
					resizeMsg, err := json.Marshal(m)
					require.Nil(t, err, "failed marshilng ctrl msg: %v", msg)
					Logger.Info("Sending the resize message")
					cdc.Send(resizeMsg)
				}

			})
		})
	})
	SignalPair(client, peer)
	select {
	case <-time.After(3 * time.Second):
		t.Error("Timeout waiting for dat ain reconnected pane")
	case <-done:
	}
}

func TestPayloadOperations(t *testing.T) {
	initTest(t)
	done := make(chan bool)
	client, cert, err := NewClient(true)
	require.Nil(t, err, "Failed to create a new client %v", err)
	peer, err := NewPeer(cert)
	require.Nil(t, err, "NewPeer failed with: %s", err)
	cdc, err := client.CreateDataChannel("%", nil)
	require.Nil(t, err, "Failed to create the control data channel: %v", err)
	payload := []byte("[\"Better payload\"]")
	cdc.OnOpen(func() {
		time.Sleep(10 * time.Millisecond)
		args := SetPayloadArgs{payload}
		setPayload := CTRLMessage{time.Now().UnixNano(), 777,
			"set_payload", &args}
		setMsg, err := json.Marshal(setPayload)
		require.Nil(t, err, "Failed to marshal the auth args: %v", err)
		Logger.Info("Sending set_payload msg")
		cdc.Send(setMsg)
	})
	cdc.OnMessage(func(msg webrtc.DataChannelMessage) {
		// we should get an ack for the auth message and the get payload
		Logger.Infof("Got a ctrl msg: %s", msg.Data)
		args := ParseAck(t, msg)
		if args.Ref == 777 {
			require.Nil(t, err, "Failed to unmarshall the control data channel: %v", err)
			require.Equal(t, payload, []byte(args.Body),
				"Got the wrong payload: %q", args.Body)
			done <- true
		}
	})
	SignalPair(client, peer)
	select {
	case <-time.After(3 * time.Second):
		t.Error("Timeout waiting for payload data")
	case <-done:
	}
	// TODO: now get_payload and make sure it's the same
}
func TestMarkerRestore(t *testing.T) {
	initTest(t)
	var (
		cID       string
		dc        *webrtc.DataChannel
		markerRef int
		marker    int
	)
	gotSetMarkerAck := make(chan bool)
	gotFirst := make(chan bool)
	gotSecondAgain := make(chan bool)
	gotAck := make(chan bool)
	client, cert, err := NewClient(true)
	require.Nil(t, err, "Failed to create a new client %v", err)
	// start the server
	peer, err := NewPeer(cert)
	require.Nil(t, err, "NewPeer failed with: %s", err)
	require.Nil(t, err, "Failed to start a new server %v", err)
	// create the command & control data channel
	cdc, err := client.CreateDataChannel("%", nil)
	require.Nil(t, err, "Failed to create the control data channel: %v", err)
	// count the incoming messages
	count := 0
	cdc.OnOpen(func() {
		Logger.Info("cdc is opened")
		cdc.OnMessage(func(msg webrtc.DataChannelMessage) {
			// we should get an ack for the auth message
			var cm CTRLMessage
			Logger.Infof("Got a ctrl msg: %s", msg.Data)
			err := json.Unmarshal(msg.Data, &cm)
			require.Nil(t, err, "Failed to marshal the server msg: %v", err)
			if cm.Type == "ack" {
				args := ParseAck(t, msg)
				if args.Ref == markerRef {
					json.Unmarshal(args.Body, &marker)
					Logger.Infof("Got marker: %d", marker)
					gotSetMarkerAck <- true
				}
			}
		})
		dc, err = client.CreateDataChannel(
			"24x80,bash,-c,echo 123456 ; sleep 1; echo 789; sleep 9", nil)
		require.Nil(t, err, "Failed to create the echo data channel: %v", err)
		dc.OnOpen(func() {
			Logger.Infof("Channel %q opened", dc.Label())
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			Logger.Infof("DC Got msg #%d: %s", count, msg.Data)
			if len(msg.Data) > 0 {
				if count == 0 {
					cID = string(msg.Data)
					Logger.Infof("Client got a channel id: %q", cID)
				}
				if count == 1 {
					require.Contains(t, string(msg.Data), "123456")
					gotFirst <- true
				}
				count++
			}
		})
	})
	SignalPair(client, peer)
	select {
	case <-time.After(6 * time.Second):
		t.Error("Timeout waiting for first datfirst data data")
	case <-gotFirst:
	}
	markerRef = getMarker(cdc)
	select {
	case <-time.After(6 * time.Second):
		t.Error("Timeout waiting for marker ack")
	case <-gotSetMarkerAck:
	}
	client2, cert, err := NewClient(true)
	require.Nil(t, err, "Failed to create the second client %v", err)
	peer2, err := NewPeer(cert)
	require.Nil(t, err, "NewPeer2 failed with: %s", err)
	require.Nil(t, err, "Failed to start a new server %v", err)
	// create the command & control data channel
	SignalPair(client2, peer2)
	cdc2, err := client2.CreateDataChannel("%", nil)
	require.Nil(t, err, "Failed to create the control data channel: %v", err)
	// count the incoming messages
	cdc2.OnOpen(func() {
		// send the restore message "marker"
		go SendRestore(cdc2, 345, marker)
		cdc2.OnMessage(func(msg webrtc.DataChannelMessage) {
			// we should get an ack for the auth message
			var cm CTRLMessage
			err := json.Unmarshal(msg.Data, &cm)
			require.Nil(t, err, "Failed to marshal the server msg: %v", err)
			Logger.Info("client2 got msg: %v", cm)
			if cm.Type == "ack" {
				gotAck <- true
			}
		})
		<-gotAck
		time.Sleep(time.Second)
		dc, err = client2.CreateDataChannel(">"+cID, nil)
		require.Nil(t, err, "Failed to create the echo data channel: %v", err)
		dc.OnOpen(func() {
			Logger.Infof("TS> Channel %q re-opened", dc.Label())
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			// ignore null messages
			if len(msg.Data) > 0 {
				require.Contains(t, string(msg.Data), "789")
				gotSecondAgain <- true
			}

		})
	})
	select {
	case <-time.After(3 * time.Second):
		t.Error("Timeout waiting for restored data")
	case <-gotSecondAgain:
	}
}
func TestAddPaneMessage(t *testing.T) {
	var wg sync.WaitGroup
	initTest(t)
	// the trinity: a new datachannel, an ack and BADWOLF (aka command output)
	wg.Add(3)
	client, cert, err := NewClient(true)
	require.Nil(t, err, "Failed to create a new client %v", err)
	peer, err := NewPeer(cert)
	require.Nil(t, err)
	done := make(chan bool)
	client.OnDataChannel(func(d *webrtc.DataChannel) {
		d.OnMessage(func(msg webrtc.DataChannelMessage) {
			Logger.Infof("Got a new datachannel message: %s", string(msg.Data))
			require.Equal(t, "BADWOLF", string(msg.Data[:7]))
			Logger.Infof(string(msg.Data))
			wg.Done()
		})
		l := d.Label()
		Logger.Infof("Got a new datachannel: %s", l)
		require.Regexp(t, regexp.MustCompile("^456:[0-9]+"), l)
		wg.Done()
	})
	cdc, err := client.CreateDataChannel("%", nil)
	require.Nil(t, err, "failed to create the control data channel: %v", err)
	cdc.OnOpen(func() {
		Logger.Info("cdc opened")
		cdc.OnMessage(func(msg webrtc.DataChannelMessage) {
			ack := ParseAck(t, msg)
			if ack.Ref == 456 {
				Logger.Infof("Got the ACK")
				wg.Done()
			}
		})
		addPaneArgs := AddPaneArgs{Rows: 12, Cols: 34,
			Command: []string{"echo", "BADWOLF"}} //, "&&", "sleep", "5"}}
		m := CTRLMessage{time.Now().UnixNano(), 456, "add_pane",
			&addPaneArgs}
		msg, err := json.Marshal(m)
		require.Nil(t, err, "failed marshilng ctrl msg: %v", msg)
		time.Sleep(time.Second / 10)
		cdc.Send(msg)
	})
	go func() {
		wg.Wait()
		done <- true
	}()
	SignalPair(client, peer)
	select {
	case <-time.After(6 * time.Second):
		t.Error("Timeout waiting for server to open channel")
	case <-done:
	}
}
func TestReconnectPane(t *testing.T) {
	var wg sync.WaitGroup
	var gotMsg sync.WaitGroup
	var ci int
	initTest(t)
	client, cert, err := NewClient(true)
	require.Nil(t, err, "Failed to create a new client %v", err)
	peer, err := NewPeer(cert)
	require.Nil(t, err)
	client.OnDataChannel(func(d *webrtc.DataChannel) {
		l := d.Label()
		//fs := strings.Split(d.Label(), ",")
		d.OnMessage(func(msg webrtc.DataChannelMessage) {
			if len(msg.Data) > 0 {
				Logger.Infof("Got a message in %s: %s", l, string(msg.Data))
				if strings.Contains(string(msg.Data), "BADWOLF") {
					gotMsg.Done()
				}
			}
		})
		Logger.Infof("Got a new datachannel: %s", l)
		require.Regexp(t, regexp.MustCompile("^45[67]:[0-9]+"), l)
		wg.Done()
	})
	cdc, err := client.CreateDataChannel("%", nil)
	require.Nil(t, err, "failed to create the control data channel: %v", err)
	cdc.OnOpen(func() {
		Logger.Info("cdc opened")
		cdc.OnMessage(func(msg webrtc.DataChannelMessage) {
			Logger.Infof("cdc got an ack: %v", string(msg.Data))
			ack := ParseAck(t, msg)
			if ack.Ref == 456 {
				ci, err = strconv.Atoi(string(ack.Body))
				require.Nil(t, err)
				wg.Done()
			}
		})
		time.Sleep(time.Second / 100)
		addPaneArgs := AddPaneArgs{Rows: 12, Cols: 34,
			Command: []string{"bash", "-c", "sleep 1; echo BADWOLF"}}
		m := CTRLMessage{time.Now().UnixNano(), 456, "add_pane",
			&addPaneArgs}
		msg, err := json.Marshal(m)
		require.Nil(t, err, "failed marshilng ctrl msg: %v", msg)
		cdc.Send(msg)
	})
	wg.Add(2)
	gotMsg.Add(2)
	SignalPair(client, peer)
	wg.Wait()
	Logger.Infof("After first wait")
	wg.Add(1)
	a := ReconnectPaneArgs{ID: ci}
	m := CTRLMessage{time.Now().UnixNano(), 457, "reconnect_pane",
		&a}
	msg, err := json.Marshal(m)
	require.Nil(t, err, "failed marshilng ctrl msg: %v", msg)
	cdc.Send(msg)
	gotMsg.Wait()
}
