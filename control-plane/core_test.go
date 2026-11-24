package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
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
	st, err := OpenStore(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
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
