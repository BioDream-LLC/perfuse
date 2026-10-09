package smartauth

import (
	"sort"
)

// The registered apps and the people who sign in can change while the server runs (Users → SMART apps in the console), so
// every request reads them through these, under the server's lock, rather than from the maps directly.

func (s *Server) client(id string) *Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Clients[id]
}

func (s *Server) user(name string) *User {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Users[name]
}

// userBySubject is the person linked to an upstream account.
func (s *Server) userBySubject(sub string) *User {
	if sub == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.Users {
		if u.OIDCSubject == sub {
			return u
		}
	}
	return nil
}

// signIn reports whether people sign in here: -smart-users was given, even if nobody is listed yet.
func (s *Server) signIn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Users != nil
}

func (s *Server) clientList() []*Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Client, 0, len(s.Clients))
	for _, c := range s.Clients {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ClientList is every registered app, by id.
func (s *Server) ClientList() []*Client { return s.clientList() }

// UserList is every person who may sign in, by username.
func (s *Server) UserList() []*User {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*User, 0, len(s.Users))
	for _, u := range s.Users {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

// SetClients replaces the registered apps. Grants already issued to an app no longer listed stop working.
func (s *Server) SetClients(c Clients) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Clients = c
}

// SetUsers replaces the people who may sign in. Refresh tokens of someone no longer listed stop working.
func (s *Server) SetUsers(u Users) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u == nil {
		u = Users{}
	}
	s.Users = u
}
