package store

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestADirectoryThatManagesChannelGroupsSetsThemAtEverySignIn(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	id := ExternalIdentity{Issuer: "https://idp.example", Subject: "u1", Username: "nurse", Role: RoleOperator,
		ManageChannelGroups: true, ChannelGroups: []string{"adt"}}

	_, u, created, err := s.SignInExternal(ctx, id, true, "127.0.0.1", "test")
	if err != nil || !created {
		t.Fatalf("first sign-in: %v created=%v", err, created)
	}
	got, _ := s.GetUserByID(ctx, u.ID)
	if !reflect.DeepEqual(got.ChannelGroups, []string{"adt"}) || got.Role != RoleOperator {
		t.Fatalf("after the first sign-in: role %s, channel groups %v", got.Role, got.ChannelGroups)
	}

	// The directory moved them: the next sign-in follows it.
	id.ChannelGroups = []string{"lab"}
	if _, _, _, err := s.SignInExternal(ctx, id, true, "127.0.0.1", "test"); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetUserByID(ctx, u.ID); !reflect.DeepEqual(got.ChannelGroups, []string{"lab"}) {
		t.Errorf("after moving groups: %v, want [lab]", got.ChannelGroups)
	}

	// A directory that does not manage channel groups leaves what an administrator set.
	if err := s.SetChannelGroups(ctx, u.ID, []string{"pharmacy"}); err != nil {
		t.Fatal(err)
	}
	id.ManageChannelGroups, id.ChannelGroups = false, nil
	if _, _, _, err := s.SignInExternal(ctx, id, true, "127.0.0.1", "test"); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetUserByID(ctx, u.ID); !reflect.DeepEqual(got.ChannelGroups, []string{"pharmacy"}) {
		t.Errorf("an unmanaged sign-in changed the channel groups: %v", got.ChannelGroups)
	}
}

func TestASessionsRoleIsItsStrongestAndEachGroupKeepsItsOwn(t *testing.T) {
	sess := newSession("", 1, "u", RoleViewer, time.Now(), []string{"lab", "adt"}, map[string]Role{"lab": RoleEditor})
	if sess.Role != RoleEditor {
		t.Errorf("session role %s, want editor (its strongest)", sess.Role)
	}
	if sess.RoleIn("lab") != RoleEditor || sess.RoleIn("adt") != RoleViewer || sess.RoleIn("pharmacy") != "" {
		t.Errorf("lab %s adt %s pharmacy %q", sess.RoleIn("lab"), sess.RoleIn("adt"), sess.RoleIn("pharmacy"))
	}
	// An unlimited account has one role, whatever is stored beside it.
	if s := newSession("", 1, "u", RoleViewer, time.Now(), nil, map[string]Role{"lab": RoleAdmin}); s.Role != RoleViewer {
		t.Errorf("an unlimited account was raised to %s by a group role", s.Role)
	}
	// Platform is never granted per group.
	if s := newSession("", 1, "u", RoleViewer, time.Now(), []string{"lab"}, map[string]Role{"lab": RolePlatform}); s.Role != RoleViewer {
		t.Errorf("a per-group platform role raised the session to %s", s.Role)
	}
}
