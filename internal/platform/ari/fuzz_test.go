package ari

import "testing"

// Whatever Asterisk (or anything on its socket) sends, decoding never panics.
func FuzzEventDecode(f *testing.F) {
	f.Add([]byte(`{"type":"StasisStart","channel":{"id":"c","caller":{"number":"+256772123456"}}}`))
	f.Add([]byte(`{"type":"ChannelDtmfReceived","digit":"#","channel":{"id":"c"}}`))
	f.Add([]byte(`{"type":"PlaybackFinished","playback":{"id":"p","target_uri":"channel:c"}}`))
	f.Add([]byte(`{"type":"PlaybackStarted","playback":{"id":"p","target_uri":"channel:c"}}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = decodeEvent(b)
	})
}
