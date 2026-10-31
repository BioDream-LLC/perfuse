package store

import (
	"context"
	"testing"
)

func TestAPITokenGroupLimit(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	tok, err := st.CreateAPIToken(ctx, "riverside-provider-access", RoleViewer, "admin")
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := st.LookupAPIToken(ctx, tok)
	if len(sess.FHIRGroups) != 0 {
		t.Fatalf("a new token is limited: %v", sess.FHIRGroups)
	}
	if err := st.LimitAPITokenToGroups(ctx, "riverside-provider-access", []string{"Group/riverside", " north "}); err != nil {
		t.Fatal(err)
	}
	sess, _ = st.LookupAPIToken(ctx, tok)
	if len(sess.FHIRGroups) != 2 || sess.FHIRGroups[0] != "riverside" || sess.FHIRGroups[1] != "north" {
		t.Fatalf("groups: %v", sess.FHIRGroups)
	}
	if err := st.LimitAPITokenToGroups(ctx, "no-such-token", []string{"x"}); err != ErrTokenNotFound {
		t.Fatalf("an unknown token was limited: %v", err)
	}
	if err := st.LimitAPITokenToGroups(ctx, "riverside-provider-access", []string{"a,b"}); err == nil {
		t.Fatal("a comma inside a Group id was accepted")
	}
}

// TestALiveTokenLabelIsUnique guards the label that revoking and limiting a token go by.
func TestALiveTokenLabelIsUnique(t *testing.T) {
	s := open(t)
	if _, err := s.CreateAPIToken(t.Context(), "lab feed", RoleViewer, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAPIToken(t.Context(), "lab feed", RoleOperator, "admin"); err == nil {
		t.Fatal("a second live token was given the same label, so revoking one would revoke both")
	}
}
