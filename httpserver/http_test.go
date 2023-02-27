package httpserver

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/stretchr/testify/require"
	"github.com/tuzig/webexec/peers"
	"go.uber.org/zap/zaptest"
)

var serverHost string

// MockAuthBackend is used to mock the auth backend
type MockAuthBackend struct {
	authorized string
}

func NewMockAuthBackend(authorized string) *MockAuthBackend {
	return &MockAuthBackend{authorized}
}

func (a *MockAuthBackend) IsAuthorized(tokens []string) bool {
	if a.authorized == "" {
		return false
	}
	for _, t := range tokens {
		if t == a.authorized {
			return true
		}
	}
	return false
}

func newClient(t *testing.T) (*webrtc.PeerConnection, *webrtc.Certificate, error) {
	secretKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	certificate, err := webrtc.GenerateCertificate(secretKey)
	certs := []webrtc.Certificate{*certificate}
	client, err := webrtc.NewPeerConnection(
		webrtc.Configuration{Certificates: certs})
	if err != nil {
		return nil, nil, err
	}
	return client, certificate, err
}
func newCert(t *testing.T) *webrtc.Certificate {
	secretKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err, "Failed to generate a secret key: %q", err)
	certificate, err := webrtc.GenerateCertificate(secretKey)
	require.NoError(t, err, "Failed to generate a certificate: %q", err)
	return certificate
}

func TestConnect(t *testing.T) {
	done := make(chan bool)
	// start the webrtc client
	client, cert, err := newClient(t)
	require.NoError(t, err, "Failed to create a client: %q", err)
	cdc, err := client.CreateDataChannel("%", nil)
	require.Nil(t, err, "Failed to create the control data channel: %q", err)
	clientOffer, err := client.CreateOffer(nil)
	require.Nil(t, err, "Failed to create client offer: %q", err)
	gatherComplete := webrtc.GatheringCompletePromise(client)
	err = client.SetLocalDescription(clientOffer)
	require.Nil(t, err, "Failed to set client's local Description client offer: %q", err)
	select {
	case <-time.After(3 * time.Second):
		t.Errorf("timed out waiting to ice gathering to complete")
	case <-gatherComplete:
		var sd webrtc.SessionDescription
		buf := make([]byte, 4096)
		l, err := peers.EncodeOffer(buf, *client.LocalDescription())
		require.Nil(t, err, "Failed ending an offer: %v", clientOffer)
		fp, err := peers.ExtractFP(cert)
		require.NoError(t, err, "Failed to extract the fingerprint: %q", err)
		p := ConnectRequest{fp, 1, string(buf[:l])}
		b, err := json.Marshal(p)
		req := httptest.NewRequest(http.MethodPost, "/connect", bytes.NewBuffer(b))
		req.RemoteAddr = "8.8.8.8"
		w := httptest.NewRecorder()
		a := NewMockAuthBackend(fp)
		logger := zaptest.NewLogger(t).Sugar()
		certificate := newCert(t)
		require.NoError(t, err, "Failed to create a certificate: %q", err)
		conf := &peers.Conf{
			Certificate:       certificate,
			Logger:            logger,
			DisconnectTimeout: time.Second,
			FailedTimeout:     time.Second,
			KeepAliveInterval: time.Second,
			GatheringTimeout:  time.Second,
		}
		h := NewConnectHandler(a, conf, logger)
		h.HandleConnect(w, req)
		require.Equal(t, http.StatusOK, w.Code)
		// read server offer
		err = peers.DecodeOffer(&sd, w.Body.Bytes())
		require.Nil(t, err, "Failed decoding an offer: %v", clientOffer)
		client.SetRemoteDescription(sd)
		// count the incoming messages
		cdc.OnOpen(func() {
			done <- true
		})
	}
	select {
	case <-time.After(3 * time.Second):
		t.Errorf("Timeouton cdc open")
	case <-done:
	}
	/*
			// There's t.Cleanup in go 1.15+
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err := Shutdown(ctx)
			require.Nil(t, err, "Failed shutting the http server: %v", err)
		Shutdown()
		// TODO: be smarter, this is just a hack to get github action to pass
		time.Sleep(500 * time.Millisecond)
	*/
}
func TestConnectBadFP(t *testing.T) {
	client, cert, err := newClient(t)
	_, err = client.CreateDataChannel("%", nil)
	require.Nil(t, err, "Failed to create the control data channel: %q", err)
	clientOffer, err := client.CreateOffer(nil)
	require.Nil(t, err, "Failed to create client offer: %q", err)
	gatherComplete := webrtc.GatheringCompletePromise(client)
	err = client.SetLocalDescription(clientOffer)
	require.Nil(t, err, "Failed to set client's local Description client offer: %q", err)
	select {
	case <-time.After(3 * time.Second):
		t.Errorf("timed out waiting to ice gathering to complete")
	case <-gatherComplete:
		buf := make([]byte, 4096)
		l, err := peers.EncodeOffer(buf, *client.LocalDescription())
		require.Nil(t, err, "Failed ending an offer: %v", clientOffer)
		fp, err := peers.ExtractFP(cert)
		require.NoError(t, err, "Failed to extract the fingerprint: %q", err)
		p := ConnectRequest{fp, 1, string(buf[:l])}
		b, err := json.Marshal(p)
		require.Nil(t, err, "Failed to marshal the connect request: %s", err)

		req := httptest.NewRequest(http.MethodPost, "/connect", bytes.NewBuffer(b))
		req.RemoteAddr = "8.8.8.8"
		w := httptest.NewRecorder()
		a := NewMockAuthBackend("")
		logger := zaptest.NewLogger(t).Sugar()
		conf := &peers.Conf{
			// Certificate:       certificate,
			Logger:            logger,
			DisconnectTimeout: time.Second,
			FailedTimeout:     time.Second,
			KeepAliveInterval: time.Second,
			GatheringTimeout:  time.Second,
		}
		h := NewConnectHandler(a, conf, logger)
		h.HandleConnect(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code)
	}
}

func TestEncodeDecodeStringArray(t *testing.T) {
	a := []string{"Hello", "World"}
	b := make([]byte, 4096)
	l, err := peers.EncodeOffer(b, a)
	require.Nil(t, err, "Failed to encode offer: %s", err)
	c := make([]string, 2)
	err = peers.DecodeOffer(&c, b[:l])
	require.Nil(t, err, "Failed to decode offer: %s", err)
	require.Equal(t, a, c)
}
