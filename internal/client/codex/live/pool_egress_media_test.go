package live

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func newBoundPoolWebRTCAPI(t *testing.T) *webrtc.API {
	t.Helper()
	api, err := newPionAPIWithOptions(config.CodexLiveMediaRelayConfig{}, false, false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return api
}

func TestPoolEgressWebRTCRejectsNewInputAfterPolicyChange(t *testing.T) {
	for _, change := range []string{"egress", "membership"} {
		for _, input := range []string{"event", "audio"} {
			t.Run(change+"/"+input, func(t *testing.T) {
				testPoolWebRTCPolicyChange(t, change, input)
			})
		}
	}
}

func testPoolWebRTCPolicyChange(t *testing.T, change, input string) {
	t.Helper()

	manager := auth.NewManager(nil, nil, nil)
	account := &auth.Auth{ID: "synthetic-account", Provider: "codex", Metadata: map[string]any{"account_id": "synthetic-principal"}}
	if _, err := manager.Register(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AccessIsolation: config.AccessIsolation{Enabled: true, Pools: []config.AccountPool{{ID: "synthetic-pool", Name: "Synthetic", Provider: "codex", Accounts: []string{auth.AccountIdentity(account)}, Egress: &config.PoolEgress{Mode: "direct-ip", SourceIP: "127.0.0.1"}}}, Keys: []config.ClientKeyBinding{{Key: "synthetic-client", Pool: "synthetic-pool"}}}}
	cfg.APIKeys = []string{"synthetic-client"}
	manager.SetConfig(cfg)
	ctx := manager.NewAccessRequest(auth.WithClientIdentity(context.Background(), "synthetic-client", "config-inline"))
	clientAPI := newBoundPoolWebRTCAPI(t)
	client, errClient := clientAPI.NewPeerConnection(webrtc.Configuration{})
	if errClient != nil {
		t.Fatalf("create client PeerConnection: %v", errClient)
	}
	defer closeTestPeerConnection(t, client)
	clientDone := make(chan struct{})
	defer close(clientDone)

	clientAudio, errTrack := webrtc.NewTrackLocalStaticRTP(opusCodec, "client-audio", "client")
	if errTrack != nil {
		t.Fatalf("create client audio track: %v", errTrack)
	}
	clientSender, errTrack := client.AddTrack(clientAudio)
	if errTrack != nil {
		t.Fatalf("add client audio track: %v", errTrack)
	}
	go drainRTCP("test-client", clientSender, clientDone)
	clientData, errData := client.CreateDataChannel(realtimeDataChannelLabel, nil)
	if errData != nil {
		t.Fatalf("create client DataChannel: %v", errData)
	}
	clientMessages := make(chan webrtc.DataChannelMessage, 4)
	clientData.OnMessage(func(message webrtc.DataChannelMessage) {
		message.Data = append([]byte(nil), message.Data...)
		clientMessages <- message
	})

	clientOffer := completeOffer(t, client)
	relayConfig := config.CodexLiveMediaRelayConfig{
		Enabled:                 true,
		MaxSessions:             1,
		DisablePrivateRemoteIPs: false,
	}
	relay, errRelay := newPionMediaRelay(relayConfig)
	if errRelay != nil {
		t.Fatalf("create media relay: %v", errRelay)
	}
	denied := make(chan error, 1)
	session, relayOffer, errSession := relay.NewSession(ctx, clientOffer, mediaSessionRoute{
		authorize: func() error {
			errAccess := manager.CheckCurrentAccount(ctx, account)
			if errAccess != nil {
				select {
				case denied <- errAccess:
				default:
				}
			}
			return errAccess
		},
		credential: "Voice credential",
		authIndex:  "auth-index",
	})
	if errSession != nil {
		t.Fatalf("create media relay session: %v", errSession)
	}
	session.SetCallID("pool-policy-test")
	closed := make(chan string, 1)
	session.SetCloseHandler(func(reason string) { closed <- reason })
	defer func() {
		if errClose := session.Close(); errClose != nil {
			t.Errorf("close media relay session: %v", errClose)
		}
	}()
	upstreamAPI := newBoundPoolWebRTCAPI(t)
	upstream, errUpstream := upstreamAPI.NewPeerConnection(webrtc.Configuration{})
	if errUpstream != nil {
		t.Fatalf("create upstream PeerConnection: %v", errUpstream)
	}
	defer closeTestPeerConnection(t, upstream)
	upstreamDone := make(chan struct{})
	defer close(upstreamDone)

	upstreamDataChannels := make(chan *webrtc.DataChannel, 1)
	upstreamMessages := make(chan webrtc.DataChannelMessage, 4)
	upstream.OnDataChannel(func(channel *webrtc.DataChannel) {
		if channel.Label() != realtimeDataChannelLabel {
			return
		}
		channel.OnMessage(func(message webrtc.DataChannelMessage) {
			message.Data = append([]byte(nil), message.Data...)
			upstreamMessages <- message
		})
		upstreamDataChannels <- channel
	})
	upstreamAudioMessages := make(chan []byte, 16)
	upstream.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			packet, _, errRead := track.ReadRTP()
			if errRead != nil {
				return
			}
			upstreamAudioMessages <- append([]byte(nil), packet.Payload...)
		}
	})
	if errRemote := upstream.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: relayOffer}); errRemote != nil {
		t.Fatalf("set upstream offer: %v", errRemote)
	}
	upstreamAudio, errTrack := webrtc.NewTrackLocalStaticRTP(opusCodec, "upstream-audio", "upstream")
	if errTrack != nil {
		t.Fatalf("create upstream audio track: %v", errTrack)
	}
	upstreamSender, errTrack := upstream.AddTrack(upstreamAudio)
	if errTrack != nil {
		t.Fatalf("add upstream audio track: %v", errTrack)
	}
	go drainRTCP("test-upstream", upstreamSender, upstreamDone)
	upstreamAnswer := completeAnswer(t, upstream)
	downstreamAnswer, errAnswer := session.AcceptUpstreamAnswer(context.Background(), upstreamAnswer)
	if errAnswer != nil {
		t.Fatalf("accept upstream answer: %v", errAnswer)
	}
	if errRemote := client.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: downstreamAnswer}); errRemote != nil {
		t.Fatalf("set client answer: %v", errRemote)
	}

	upstreamData := receiveDataChannel(t, upstreamDataChannels)
	waitDataChannelOpen(t, clientData)
	waitDataChannelOpen(t, upstreamData)

	// Establish both directions before testing the changed grant.
	if errSend := clientData.SendText(`{"type":"response.create"}`); errSend != nil {
		t.Fatal(errSend)
	}
	receiveDataMessage(t, upstreamMessages)
	sendTestRTP(t, clientAudio, []byte{0xf8, 0xff, 0xfe}, upstreamAudioMessages)

	next := cfg.CloneForRuntime()
	if change == "egress" {
		next.AccessIsolation.Pools[0].Egress.SourceIP = "127.0.0.2"
	} else {
		next.AccessIsolation.Pools[0].Accounts = nil
	}
	manager.PublishAccessPolicy(next)

	// Already active upstream output may still finish through its captured route.
	if errSend := upstreamData.SendText(`{"type":"response.done"}`); errSend != nil {
		t.Fatal(errSend)
	}
	if got := receiveDataMessage(t, clientMessages); string(got.Data) != `{"type":"response.done"}` {
		t.Fatalf("active output = %s", got.Data)
	}

	if input == "event" {
		if errSend := clientData.SendText(`{"type":"response.create"}`); errSend != nil {
			t.Fatal(errSend)
		}
	} else {
		packet := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: 100, Timestamp: 96000, SSRC: 1234}, Payload: []byte{0xf8, 0xfe, 0xfd}}
		if errWrite := clientAudio.WriteRTP(packet); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	select {
	case errAccess := <-denied:
		var grantError *auth.Error
		want := "egress_changed"
		if change == "membership" {
			want = "access_denied"
		}
		if !errors.As(errAccess, &grantError) || grantError.Code != want {
			t.Fatalf("authorization error = %v, want %s", errAccess, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("new WebRTC input did not check current policy")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("revoked media session did not close")
	}
	select {
	case message := <-upstreamMessages:
		t.Fatalf("new operation reached old media session: %s", message.Data)
	default:
	}
	for {
		select {
		case payload := <-upstreamAudioMessages:
			if string(payload) == string([]byte{0xf8, 0xfe, 0xfd}) {
				t.Fatal("new audio reached old media session")
			}
		default:
			return
		}
	}
}
