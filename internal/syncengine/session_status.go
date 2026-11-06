package syncengine

// Called under mu, or during construction before the runtime is published.
// Keep dashboard reads independent of the scan/transfer serialization lock.
func (s *sessionRuntime) publishMembers() {
	view := make([]MemberStatus, 0, len(s.state.Members))
	for id, m := range s.state.Members {
		st := s.memberStatus[id]
		st.ID = id
		st.Name = m.Name
		if id == s.self.ID {
			st.Local = true
			st.State = "This computer"
		} else if st.State == "" {
			st.State = "Waiting for connection"
		}
		view = append(view, st)
	}
	s.viewMu.Lock()
	s.memberView = view
	s.viewMu.Unlock()
}
