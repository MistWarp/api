package roturwebhook

import "testing"

// The example from Rotur's webhook docs, signed the way Rotur signs it.
func TestVerify(t *testing.T) {
	const secret = "whsec_test"
	body := `{"id":"evt_1","type":"user.deleted","created_at":1790000000,"app":"app_x","data":{"user_id":"u1"}}`
	signature := Sign(secret, "evt_1", "1790000000", body)
	if signature[:3] != "v1=" || len(signature) != 67 {
		t.Fatalf("unexpected signature %q", signature)
	}
	if !VerifyAt(secret, "evt_1", "1790000000", body, signature, 1790000100) {
		t.Fatal("a fresh, correctly signed delivery was refused")
	}
	cases := map[string]bool{
		"wrong secret":    VerifyAt("whsec_other", "evt_1", "1790000000", body, signature, 1790000100),
		"changed body":    VerifyAt(secret, "evt_1", "1790000000", body+" ", signature, 1790000100),
		"other event id":  VerifyAt(secret, "evt_2", "1790000000", body, signature, 1790000100),
		"too old":         VerifyAt(secret, "evt_1", "1790000000", body, signature, 1790000301),
		"from the future": VerifyAt(secret, "evt_1", "1790000000", body, signature, 1789999699),
		"no secret":       VerifyAt("", "evt_1", "1790000000", body, signature, 1790000100),
		"bad timestamp":   VerifyAt(secret, "evt_1", "soon", body, signature, 1790000100),
	}
	for name, accepted := range cases {
		if accepted {
			t.Errorf("%s: accepted", name)
		}
	}
}
