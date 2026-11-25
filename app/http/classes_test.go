package http

import "testing"

func TestDecideIsTheCompletePolicy(t *testing.T) {
	type in struct{ local, session, anonymousOK, keyOK bool }
	bools := []bool{false, true}
	want := func(class Class, i in) verdict {
		switch class {
		case ClassPublic:
			return verdictAllow
		case ClassProtected:
			if i.session || i.anonymousOK {
				return verdictAllow
			}
			return verdictUnauthorized
		case ClassAlwaysProtected:
			if i.session {
				return verdictAllow
			}
			return verdictUnauthorized
		case ClassLocalOnly:
			if !i.local {
				return verdictForbidden
			}
			if i.session {
				return verdictAllow
			}
			return verdictUnauthorized
		case ClassLLM:
			if i.keyOK {
				return verdictAllow
			}
			return verdictUnauthorized
		case ClassPage:
			if i.session || i.anonymousOK {
				return verdictAllow
			}
			return verdictLogin
		}
		return verdictDeny
	}
	for class := ClassPublic; class <= ClassPage; class++ {
		for _, local := range bools {
			for _, session := range bools {
				for _, anon := range bools {
					for _, key := range bools {
						i := in{local, session, anon, key}
						if got := decide(class, local, session, anon, key); got != want(class, i) {
							t.Errorf("class %d %+v: got %d, want %d", class, i, got, want(class, i))
						}
					}
				}
			}
		}
	}
}

func TestDecideDeniesAnUnknownClass(t *testing.T) {
	for _, class := range []Class{0, -1, ClassPage + 1, 99} {
		if got := decide(class, true, true, true, true); got != verdictDeny {
			t.Errorf("class %d: got %d, want deny", class, got)
		}
	}
}

func TestNothingIsAllowedByTheZeroVerdict(t *testing.T) {
	var v verdict
	if v == verdictAllow {
		t.Fatal("the zero verdict must not allow")
	}
}

func TestLocalOnlyNeedsALocalCallerAndASessionWhateverLoginIsSetTo(t *testing.T) {
	if decide(ClassLocalOnly, true, false, true, true) == verdictAllow {
		t.Fatal("a local caller without a session got in")
	}
	if decide(ClassLocalOnly, false, true, true, true) == verdictAllow {
		t.Fatal("a remote caller with a session got in")
	}
}

func TestClassValidity(t *testing.T) {
	for _, c := range []Class{ClassPublic, ClassProtected, ClassAlwaysProtected, ClassLocalOnly, ClassLLM, ClassPage} {
		if !c.valid() {
			t.Errorf("class %d should be valid", c)
		}
	}
	for _, c := range []Class{0, -3, ClassPage + 1} {
		if c.valid() {
			t.Errorf("class %d should be invalid", c)
		}
	}
}

func TestUnroutedClass(t *testing.T) {
	cases := map[string]Class{
		"/v1/unknown":      ClassLLM,
		"/v1":              ClassLLM,
		"/v1beta/x":        ClassLLM,
		"/api/nope":        ClassProtected,
		"/api":             ClassProtected,
		"/dashboard/x":     ClassPage,
		"/dashboard":       ClassPage,
		"/anything":        ClassPublic,
		"/":                ClassPublic,
		"/v1x":             ClassPublic,
		"/apix":            ClassPublic,
		"/dashboardx/home": ClassPublic,
	}
	for path, want := range cases {
		if got := unroutedClass(path); got != want {
			t.Errorf("unroutedClass(%q) = %d, want %d", path, got, want)
		}
	}
}

// A client tool asks its provider how much it has used, and it carries an API
// key, not a dashboard cookie. Those paths are the client API's even when they
// sit under /api, so they get the key check, CORS and the client body cap.
func TestAProviderUsagePathIsAClientRouteAndADashboardOneIsNot(t *testing.T) {
	for _, p := range []string{"/api/tags", "/api/oauth/usage", "/v1/organizations/org-1/usage", "/v1/usage"} {
		if !IsClientAPI(p) {
			t.Errorf("%s is not a client route", p)
		}
	}
	for _, p := range []string{"/api/oauth/usage/x", "/api/oauth/status", "/api/oauth/claude/authorize", "/api/keys", "/api/usage/stats"} {
		if IsClientAPI(p) {
			t.Errorf("%s was taken for a client route", p)
		}
	}
}

func TestTheCallsThatWaitOnTheNetworkOutliveTheDefaultWriteDeadline(t *testing.T) {
	for _, p := range []string{"/api/about/upgrade", "/api/skills/sync", "/api/providers/x/test-models"} {
		if !IsLongDashboardCall(p) {
			t.Errorf("%s would be cut at the default write deadline", p)
		}
	}
	for _, p := range []string{"/api/keys", "/api/about/update", "/api/skills"} {
		if IsLongDashboardCall(p) {
			t.Errorf("%s was given the long deadline", p)
		}
	}
}
