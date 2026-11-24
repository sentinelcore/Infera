package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Stripe prepaid credits. Developers buy credits through Stripe Checkout; the webhook adds them.
//
//	STRIPE_SECRET_KEY      sk_test_… or sk_live_…
//	STRIPE_WEBHOOK_SECRET  whsec_… (from the webhook endpoint you add in Stripe for /api/stripe/webhook)
//	INFERA_PUBLIC_URL      e.g. https://beta.example.com (used for the return links)
type Billing struct {
	st        *Store
	secretKey string
	whSecret  string
	publicURL string
	apiBase   string
	http      *http.Client
}

func NewBilling(st *Store) *Billing {
	return &Billing{st: st, secretKey: os.Getenv("STRIPE_SECRET_KEY"), whSecret: os.Getenv("STRIPE_WEBHOOK_SECRET"),
		publicURL: strings.TrimRight(os.Getenv("INFERA_PUBLIC_URL"), "/"), apiBase: env("STRIPE_API_BASE", "https://api.stripe.com"),
		http: &http.Client{Timeout: 20 * time.Second}}
}

func (b *Billing) Enabled() bool { return b.secretKey != "" && b.whSecret != "" }

// POST /api/billing/checkout {usd} (customer) -> {url}
func (b *Billing) Checkout(w http.ResponseWriter, r *http.Request) {
	acct, err := b.st.AccountByKey(bearer(r))
	if err != nil || acct.Kind != "customer" {
		apiError(w, 401, "sign in with your API key")
		return
	}
	if !b.Enabled() {
		apiError(w, 503, "card payments aren't set up on this server yet")
		return
	}
	var in struct {
		USD float64 `json:"usd"`
	}
	if readJSON(r, &in) != nil || in.USD < 5 || in.USD > 5000 {
		apiError(w, 400, "choose an amount between $5 and $5,000")
		return
	}
	base := b.publicURL
	if base == "" {
		base = "http://" + r.Host
	}
	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", "usd")
	form.Set("line_items[0][price_data][unit_amount]", strconv.Itoa(int(in.USD*100+0.5)))
	form.Set("line_items[0][price_data][product_data][name]", "Infera inference credits")
	form.Set("client_reference_id", acct.ID)
	form.Set("metadata[account_id]", acct.ID)
	form.Set("customer_email", acct.Email)
	form.Set("success_url", base+"/?paid=1#dev")
	form.Set("cancel_url", base+"/#dev")
	req, _ := http.NewRequestWithContext(r.Context(), "POST", b.apiBase+"/v1/checkout/sessions", strings.NewReader(form.Encode()))
	req.SetBasicAuth(b.secretKey, "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := b.http.Do(req)
	if err != nil {
		apiError(w, 502, "couldn't reach Stripe")
		return
	}
	defer resp.Body.Close()
	var out struct {
		URL   string `json:"url"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 || out.URL == "" {
		apiError(w, 502, "Stripe error: "+out.Error.Message)
		return
	}
	writeJSON(w, map[string]string{"url": out.URL})
}

// POST /api/stripe/webhook — credits the account when a Checkout Session is paid.
func (b *Billing) Webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || !b.Enabled() {
		http.Error(w, "unavailable", 503)
		return
	}
	if err := verifyStripeSignature(body, r.Header.Get("Stripe-Signature"), b.whSecret, time.Now(), 5*time.Minute); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var ev struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			Object struct {
				ID                string            `json:"id"`
				PaymentStatus     string            `json:"payment_status"`
				AmountTotal       int64             `json:"amount_total"`
				Currency          string            `json:"currency"`
				ClientReferenceID string            `json:"client_reference_id"`
				Metadata          map[string]string `json:"metadata"`
			} `json:"object"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &ev) != nil {
		http.Error(w, "bad json", 400)
		return
	}
	if ev.Type != "checkout.session.completed" && ev.Type != "checkout.session.async_payment_succeeded" {
		w.WriteHeader(200)
		return
	}
	o := ev.Data.Object
	if o.PaymentStatus != "paid" || o.Currency != "usd" {
		w.WriteHeader(200)
		return
	}
	acctID := o.Metadata["account_id"]
	if acctID == "" {
		acctID = o.ClientReferenceID
	}
	if err := b.st.CreditFromStripe(o.ID, acctID, o.AmountTotal*microUSD/100); err != nil {
		log.Printf("stripe webhook %s: %v", ev.ID, err)
	}
	w.WriteHeader(200)
}

// verifyStripeSignature checks the Stripe-Signature header: t=<ts>,v1=<hex hmac of "t.body">.
func verifyStripeSignature(body []byte, header, secret string, now time.Time, tolerance time.Duration) error {
	var ts int64
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			ts, _ = strconv.ParseInt(kv[1], 10, 64)
		case "v1":
			sigs = append(sigs, kv[1])
		}
	}
	if ts == 0 || len(sigs) == 0 {
		return fmt.Errorf("missing signature")
	}
	if d := now.Sub(time.Unix(ts, 0)); d > tolerance || d < -tolerance {
		return fmt.Errorf("signature too old")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	for _, s := range sigs {
		if hmac.Equal([]byte(s), []byte(want)) {
			return nil
		}
	}
	return fmt.Errorf("signature mismatch")
}
