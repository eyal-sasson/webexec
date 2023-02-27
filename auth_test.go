package main

import (
	"io/ioutil"
	"testing"

	"github.com/stretchr/testify/require"
)

// it doesn't seem like SignalPair works when we need to test at this level.
/*
func TestWrongFingerprint(t *testing.T) {
	// initTest(t)
	failed := make(chan bool)
	// create an unknown client
	client, certificate, err := NewClient(false)
	require.Nil(t, err, "Failed to create a new client %v", err)
	peer, err := peers.NewPeer(&peers.Conf{Logger: Logger,
		Certificate: certificate})
	require.NoError(t, err, "NewPeer failed with: %s", err)
	require.NotNil(t, peer, "NewPeer returned nil")
	dc, err := client.CreateDataChannel("echo,Failed", nil)
	require.NoError(t, err, "failed to create the a channel: %q", err)
	dc.OnMessage(func(_ webrtc.DataChannelMessage) { failed <- true })
	require.Nil(t, err, "failed to signal pair: %q", err)
	select {
	case <-time.After(3 * time.Second):
	case <-failed:
		t.Error("Data channel is opened even though no authentication")
	}
	// peers.Shutdown()
}
*/

func TestIsAuthorized(t *testing.T) {
	// create the token file and test good & bad tokens
	initTest(t)
	file, err := ioutil.TempFile("", "authorized_fingerprints")
	require.NoError(t, err, "Failed to create a temp tokens file: %s", err)
	file.WriteString("GOODTOKEN\nANOTHERGOODTOKEN\n")
	file.Close()
	a := NewFileAuth(file.Name())
	require.True(t, a.IsAuthorized([]string{"GOODTOKEN"}))
	require.False(t, a.IsAuthorized([]string{"BADTOKEN"}))
	require.True(t, a.IsAuthorized([]string{"BADTOKEN", "GOODTOKEN"}))
	require.True(t, a.IsAuthorized([]string{"GOODTOKEN", "BADTOKEN"}))
	require.True(t, a.IsAuthorized([]string{"ANOTHERGOODTOKEN"}))
}
