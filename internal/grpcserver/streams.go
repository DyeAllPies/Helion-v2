// internal/grpcserver/streams.go
//
// Active heartbeat stream bookkeeping: register, unregister, cancel.

package grpcserver

// CancelStream forcibly closes the active heartbeat stream for nodeID by
// closing its done channel.  The Heartbeat loop checks the channel on each
// iteration and returns codes.Unauthenticated when it is closed.
// Implements cluster.StreamRevoker so the Registry can wire it in at startup.
func (s *Server) CancelStream(nodeID string) {
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	if ch, ok := s.streams[nodeID]; ok {
		close(ch)
		delete(s.streams, nodeID)
	}
}

// registerStream stores a done channel for nodeID's heartbeat stream.
// If a prior channel exists (e.g. reconnected node), it is closed first.
func (s *Server) registerStream(nodeID string, ch chan struct{}) {
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	if old, ok := s.streams[nodeID]; ok {
		close(old)
	}
	s.streams[nodeID] = ch
}

// unregisterStream removes nodeID's channel if it still matches ch.
// Guards against a race where CancelStream already deleted and a new stream
// re-registered under the same nodeID before this deferred cleanup runs.
func (s *Server) unregisterStream(nodeID string, ch chan struct{}) {
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	if s.streams[nodeID] == ch {
		delete(s.streams, nodeID)
	}
}
