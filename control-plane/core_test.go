package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
)

func TestBands(t *testing.T) {
	cases := map[float64]string{95: "90–100", 70: "70–89", 62: "50–69", 35: "30–49", 10: "0–29"}
	for score, want := range cases {
		if got := BandFor(score).Name; got != want {
			t.Errorf("score %v: band %s, want %s", score, got, want)
		}
	}
	if !BandFor(55).BatchOnly || BandFor(75).BatchOnly {
		t.Error("batch-only rule wrong")
	}
}

func TestSealRoundTrip(t *testing.T) {
	devPriv := make([]byte, 32)
	rand.Read(devPriv)
	devPub, _ := curve25519.X25519(devPriv, curve25519.Basepoint)
	s, err := NewSeal("job_1", b64(devPub))
	if err != nil {
		t.Fatal(err)
	}
	n, ct, _ := s.SealDown([]byte("hello"))
	if len(n) != 12 || len(ct) == 0 {
		t.Fatal("bad seal output")
	}
	// The up direction must reject tampered ciphertext.
	if _, err := s.OpenUp(n, append(ct[:len(ct)-1], ct[len(ct)-1]^1)); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}

func TestReceiptSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	r := "job_1|m|abc|1|2|3"
	sig := ed25519.Sign(priv, []byte(r))
	if !VerifyReceipt(b64(pub), r, b64(sig)) || VerifyReceipt(b64(pub), r+"x", b64(sig)) {
		t.Fatal("receipt verification wrong")
	}
}

func TestPenalties(t *testing.T) {
	st := mustStore(t)
	acct, _, _ := st.CreateAccount("o@x.dev", "owner", 0)
	code, _ := st.CreatePairCode(acct.ID)
	id, _, err := st.ConsumePairCode(code, "box", "pk", "linux")
	if err != nil {
		t.Fatal(err)
	}
	dep := &Dependability{st: st}
	dev, _ := st.Device(id)
	dep.Penalize(dev, "j1", CauseOwnerAction, 1000, "quit")
	dev, _ = st.Device(id)
	if dev.Dependability != 60 {
		t.Fatalf("owner action: score %v, want 60", dev.Dependability)
	}
	if b := st.OwnerBalance(acct.ID); b.Penalties != 2000 {
		t.Fatalf("owner action debit %d, want 2000", b.Penalties)
	}
	// Device failures in the first week are not debited.
	dep.Penalize(dev, "j2", CauseDeviceFailure, 1000, "wifi")
	if b := st.OwnerBalance(acct.ID); b.Penalties != 2000 {
		t.Fatalf("first-week device failure was debited")
	}
	// Three owner aborts in 24 hours suspend the device.
	dev, _ = st.Device(id)
	dep.Penalize(dev, "j3", CauseOwnerAction, 1000, "quit")
	dev, _ = st.Device(id)
	dep.Penalize(dev, "j4", CauseOwnerAction, 1000, "quit")
	dev, _ = st.Device(id)
	if dev.SuspendedUntil <= now() {
		t.Fatal("device not suspended after 3 owner aborts")
	}
	// Rewards: +0.5 for a short job.
	before := dev.Dependability
	dep.Reward(dev, "j5", 10*time.Second)
	dev, _ = st.Device(id)
	if dev.Dependability != before+0.5 {
		t.Fatalf("reward: %v -> %v", before, dev.Dependability)
	}
}

func TestDecay(t *testing.T) {
	st := mustStore(t)
	acct, _, _ := st.CreateAccount("o@x.dev", "owner", 0)
	code, _ := st.CreatePairCode(acct.ID)
	id, _, _ := st.ConsumePairCode(code, "box", "pk", "linux")
	dep := &Dependability{st: st}
	dev, _ := st.Device(id)
	dep.Penalize(dev, "j1", CauseOwnerAction, 0, "quit") // 70 -> 60
	if n := dep.DecayOnce(now() + 10*86400); n != 0 {
		t.Fatal("decayed too early")
	}
	dep.DecayOnce(now() + 31*86400)
	dev, _ = st.Device(id)
	if dev.Dependability != 65 {
		t.Fatalf("after 30 days: %v, want 65", dev.Dependability)
	}
	dep.DecayOnce(now() + 31*86400) // no double counting
	dep.DecayOnce(now() + 91*86400)
	dev, _ = st.Device(id)
	if dev.Dependability != 70 {
		t.Fatalf("after 90 days: %v, want 70", dev.Dependability)
	}
}

func TestCanaryJudging(t *testing.T) {
	st := mustStore(t)
	acct, _, _ := st.CreateAccount("o@x.dev", "owner", 0)
	code, _ := st.CreatePairCode(acct.ID)
	id, _, _ := st.ConsumePairCode(code, "box", "pk", "linux")
	g := &Gateway{st: st, hub: NewHub(st), dep: &Dependability{st: st}}
	dev, _ := st.Device(id)
	g.judgeCanary(dev, "c1", "The answer is 42.", "42", 0)
	g.judgeCanary(dev, "c2", "banana", "42", 0)
	dev, _ = st.Device(id)
	if dev.Dependability != 70 {
		t.Fatalf("one fail should not penalize: %v", dev.Dependability)
	}
	g.judgeCanary(dev, "c3", "no idea", "paris", 0)
	dev, _ = st.Device(id)
	if dev.Dependability != 55 {
		t.Fatalf("two fails in three should penalize -15: %v", dev.Dependability)
	}
}

func TestStripeWebhook(t *testing.T) {
	st := mustStore(t)
	acct, _, _ := st.CreateAccount("d@x.dev", "customer", 0)
	b := &Billing{st: st, secretKey: "sk_test", whSecret: "whsec_test"}
	body := []byte(`{"id":"evt_1","type":"checkout.session.completed","data":{"object":{"id":"cs_1","payment_status":"paid","amount_total":2500,"currency":"usd","metadata":{"account_id":"` + acct.ID + `"}}}}`)
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte("whsec_test"))
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body)
	good := fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
	send := func(sig string) int {
		r := httptest.NewRequest("POST", "/api/stripe/webhook", bytes.NewReader(body))
		r.Header.Set("Stripe-Signature", sig)
		w := httptest.NewRecorder()
		b.Webhook(w, r)
		return w.Code
	}
	if send("t=1,v1=bad") != 400 {
		t.Fatal("bad signature accepted")
	}
	if send(good) != 200 || send(good) != 200 { // delivered twice
		t.Fatal("good webhook rejected")
	}
	a, _ := st.AccountByID(acct.ID)
	if a.Credits != 25*microUSD {
		t.Fatalf("credits %d, want $25 credited once", a.Credits)
	}
}

// testStore uses SQLite, or a fresh Postgres schema when INFERA_TEST_DATABASE_URL is set.
func testStore(t *testing.T) (*Store, error) {
	url := os.Getenv("INFERA_TEST_DATABASE_URL")
	if url == "" {
		return OpenStore(filepath.Join(t.TempDir(), "t.db"))
	}
	schema := "t_" + randomHex(6)
	admin, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	defer admin.Close()
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		return nil, err
	}
	t.Cleanup(func() {
		if a, err := sql.Open("pgx", url); err == nil {
			a.Exec("DROP SCHEMA " + schema + " CASCADE")
			a.Close()
		}
	})
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	return OpenStore(url + sep + "search_path=" + schema)
}

func mustStore(t *testing.T) *Store {
	st, err := testStore(t)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestRerunComparison(t *testing.T) {
	if !outputsAgree("", "The capital of France is Paris.", "The capital of France is Paris!") {
		t.Fatal("same answer should agree")
	}
	if outputsAgree("", "The capital of France is Paris.", "Bananas are yellow and grow on trees.") {
		t.Fatal("different answers should not agree")
	}
	a := `{"data":[{"embedding":[1,0,0]},{"embedding":[0,1,0]}]}`
	b := `{"data":[{"embedding":[0.99,0.01,0]},{"embedding":[0,1,0.02]}]}`
	c := `{"data":[{"embedding":[0,0,1]},{"embedding":[1,0,0]}]}`
	if !outputsAgree("embeddings", a, b) || outputsAgree("embeddings", a, c) {
		t.Fatal("embedding comparison wrong")
	}
}

func TestRerunPenalty(t *testing.T) {
	st := mustStore(t)
	acct, _, _ := st.CreateAccount("o@x.dev", "owner", 0)
	code, _ := st.CreatePairCode(acct.ID)
	id, _, _ := st.ConsumePairCode(code, "box", "pk", "linux")
	g := &Gateway{st: st, hub: NewHub(st), dep: &Dependability{st: st}}
	r := &Rerun{gw: g}
	r.mismatch(id, "disagreed", 0)
	if d, _ := st.Device(id); d.Dependability != 70 {
		t.Fatal("one mismatch should not penalize")
	}
	r.mismatch(id, "disagreed", 0)
	if d, _ := st.Device(id); d.Dependability != 55 {
		t.Fatalf("two mismatches in five should penalize: %v", d.Dependability)
	}
}

func TestWaitlistReferrals(t *testing.T) {
	st := mustStore(t)
	a := &API{st: st, hub: NewHub(st), adminToken: "adm"}
	join := func(body string) WaitlistEntry {
		r := httptest.NewRequest("POST", "/api/waitlist", strings.NewReader(body))
		w := httptest.NewRecorder()
		a.JoinWaitlist(w, r)
		var e WaitlistEntry
		json.Unmarshal(w.Body.Bytes(), &e)
		return e
	}
	first := join(`{"email":"a@x.dev","kind":"contributor"}`)
	second := join(`{"email":"b@x.dev","kind":"developer"}`)
	if first.Position != 1 || second.Position != 2 {
		t.Fatalf("positions %d %d", first.Position, second.Position)
	}
	join(`{"email":"c@x.dev","ref":"` + second.Code + `"}`)
	again := join(`{"email":"b@x.dev"}`) // joining again returns the same spot
	if again.Code != second.Code || again.Referrals != 1 || again.Position != 1 {
		t.Fatalf("referral should move b to the front: %+v", again)
	}
	r := httptest.NewRequest("POST", "/api/admin/waitlist/invite", strings.NewReader(`{"count":1}`))
	r.Header.Set("Authorization", "Bearer adm")
	w := httptest.NewRecorder()
	a.AdminWaitlist(w, r)
	if !strings.Contains(w.Body.String(), "b@x.dev") {
		t.Fatalf("invite batch should be b: %s", w.Body.String())
	}
}

func TestRateLimit(t *testing.T) {
	l := NewLimiter(60, 2) // burst = 15
	ok := 0
	var releases []func()
	for i := 0; i < 20; i++ {
		w := httptest.NewRecorder()
		if rel := l.Acquire(w, "k"); rel != nil {
			ok++
			releases = append(releases, rel)
			if len(releases) == 2 { // stay under the in-flight cap
				for _, r := range releases {
					r()
				}
				releases = nil
			}
		} else if w.Code != 429 || w.Header().Get("Retry-After") == "" {
			t.Fatal("expected 429 with Retry-After")
		}
	}
	if ok != 15 {
		t.Fatalf("burst should allow 15, got %d", ok)
	}
	l2 := NewLimiter(1000, 1)
	r1 := l2.Acquire(httptest.NewRecorder(), "k")
	if l2.Acquire(httptest.NewRecorder(), "k") != nil {
		t.Fatal("second concurrent request should be refused")
	}
	r1()
	if l2.Acquire(httptest.NewRecorder(), "k") == nil {
		t.Fatal("after release a request should pass")
	}
}
