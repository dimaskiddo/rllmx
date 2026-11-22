package conversation

import (
	"fmt"
	"testing"
	"time"
)

func TestSkillSessionsExpireAndEvict(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := NewSkillSessions(func() time.Time { return now })
	s.Mark("a", []string{"local-one"})
	if got := s.Injected("a"); !got["local-one"] {
		t.Fatalf("state %v", got)
	}
	now = now.Add(sessionLife + time.Second)
	if got := s.Injected("a"); got != nil {
		t.Fatalf("an expired entry survived: %v", got)
	}
	for i := range maxSessions + 1 {
		s.Mark(fmt.Sprintf("k%d", i), []string{"local-x"})
	}
	if len(s.byKey) != maxSessions {
		t.Fatalf("table holds %d", len(s.byKey))
	}
	if got := s.Injected("k0"); got != nil {
		t.Fatal("the oldest entry was not evicted")
	}
	if got := s.Injected(fmt.Sprintf("k%d", maxSessions)); !got["local-x"] {
		t.Fatal("the newest entry is missing")
	}
}

func TestSkillSessionsCapTheSkillList(t *testing.T) {
	s := NewSkillSessions(nil)
	ids := make([]string, 0, maxSessionSkills+10)
	for i := range maxSessionSkills + 10 {
		ids = append(ids, fmt.Sprintf("local-%d", i))
	}
	s.Mark("a", ids)
	if got := s.Injected("a"); len(got) != maxSessionSkills {
		t.Fatalf("skills %d", len(got))
	}
}

func TestSkillSessionsSeparateCallers(t *testing.T) {
	s := NewSkillSessions(nil)
	one := SessionKey("key-one", "session")
	two := SessionKey("key-two", "session")
	s.Mark(one, []string{"local-simple"})
	if got := s.Injected(two); got != nil {
		t.Fatalf("another caller read the state: %v", got)
	}
	if one == two {
		t.Fatal("the caller id is not part of the key")
	}
	if SessionKey("k", "") != "" {
		t.Fatal("a missing session id made a key")
	}
}

func TestSkillSessionsRememberAFailover(t *testing.T) {
	s := NewSkillSessions(nil)
	if s.HadFailover("a") {
		t.Fatal("a fresh conversation failed over")
	}
	s.Failover("a")
	if !s.HadFailover("a") {
		t.Fatal("the failover was not recorded")
	}
	if s.HadFailover("b") {
		t.Fatal("a failover leaked to another conversation")
	}
}

func TestANilSkillSessionsIsQuiet(t *testing.T) {
	var s *SkillSessions
	s.Mark("a", []string{"x"})
	s.Failover("a")
	if s.Injected("a") != nil || s.HadFailover("a") {
		t.Fatal("a nil table answered")
	}
	if got := NewSkillSessions(nil); got == nil || got.Injected("") != nil {
		t.Fatal("an empty key answered")
	}
}
