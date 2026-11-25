// Infera control plane: OpenAI-compatible gateway, node hub, scheduler, ledger and dashboard.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envFloat(k string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(k), 64); err == nil {
		return v
	}
	return def
}

func main() {
	st, err := OpenStore(env("INFERA_DATABASE_URL", env("DATABASE_URL", env("INFERA_DB", "infera.db"))))
	if err != nil {
		log.Fatal(err)
	}
	cfg := &Config{
		OwnerShare:    envFloat("INFERA_OWNER_SHARE", 0.70),
		Prices:        map[string]Price{},
		DefaultPrice:  Price{In: envFloat("INFERA_PRICE_IN", 0.10), Out: envFloat("INFERA_PRICE_OUT", 0.30)},
		EmbedPrice:    envFloat("INFERA_PRICE_EMBED", 0.02),
		AudioPrice:    envFloat("INFERA_PRICE_AUDIO_MIN", 0.003),
		BatchDiscount: envFloat("INFERA_BATCH_DISCOUNT", 0.5),
		FallbackURL:   os.Getenv("INFERA_FALLBACK_URL"),
		FallbackKey:   os.Getenv("INFERA_FALLBACK_KEY"),
		FallbackModel: os.Getenv("INFERA_FALLBACK_MODEL"),
		FirstToken:    time.Duration(envFloat("INFERA_FIRST_TOKEN_S", 90)) * time.Second,
		JobDeadline:   time.Duration(envFloat("INFERA_JOB_DEADLINE_S", 600)) * time.Second,
		HedgeAfter:    time.Duration(envFloat("INFERA_HEDGE_MS", 2500)) * time.Millisecond,
	}
	if p := os.Getenv("INFERA_PRICES"); p != "" { // e.g. {"qwen3.5-2b":{"in":0.05,"out":0.15}}
		if err := json.Unmarshal([]byte(p), &cfg.Prices); err != nil {
			log.Fatalf("INFERA_PRICES: %v", err)
		}
	}
	st.PairReuse = os.Getenv("INFERA_PAIR_CODE_REUSE") == "1"
	hub := NewHub(st)
	dep := &Dependability{st: st}
	canaryAcct, err := EnsureCanaryAccount(st)
	if err != nil {
		log.Fatal(err)
	}
	gw := &Gateway{st: st, hub: hub, dep: dep, cfg: cfg, canary: canaryAcct}
	gw.limit = NewLimiter(envFloat("INFERA_RATE_RPM", 120), int(envFloat("INFERA_MAX_IN_FLIGHT", 8)))
	gw.rerun = &Rerun{gw: gw, rate: envFloat("INFERA_RERUN_RATE", 0.03)}
	go NewCanary(gw, time.Duration(envFloat("INFERA_CANARY_MINUTES", 30)*float64(time.Minute))).Loop()
	go dep.DecayLoop()
	go gw.BatchWorker()
	go mon.Watch(gw)
	go RunSanctions()
	api := &API{st: st, hub: hub, cfg: cfg, inviteCode: os.Getenv("INFERA_INVITE_CODE"), adminToken: os.Getenv("INFERA_ADMIN_TOKEN"),
		starterCents: int64(envFloat("INFERA_STARTER_CREDITS_USD", 5) * 100)}

	billing := NewBilling(st)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/billing/checkout", billing.Checkout)
	mux.HandleFunc("POST /api/stripe/webhook", billing.Webhook)
	mux.HandleFunc("GET /api/billing", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"card_payments": billing.Enabled()})
	})
	mux.HandleFunc("POST /v1/chat/completions", gw.ChatCompletions)
	mux.HandleFunc("GET /v1/models", gw.Models)
	mux.HandleFunc("POST /v1/embeddings", gw.Embeddings)
	mux.HandleFunc("POST /v1/audio/transcriptions", gw.Transcriptions)
	mux.HandleFunc("POST /v1/batch", gw.BatchSubmit)
	mux.HandleFunc("GET /v1/batch", gw.BatchList)
	mux.HandleFunc("GET /v1/batch/{id}", gw.BatchGet)
	mux.HandleFunc("GET /ws/node", hub.ServeNode)
	mux.HandleFunc("POST /api/signup", api.Signup)
	mux.HandleFunc("POST /api/pair-code", api.PairCode)
	mux.HandleFunc("POST /api/devices/pair", api.PairDevice)
	mux.HandleFunc("GET /api/owner", api.Owner)
	mux.HandleFunc("POST /api/owner/payout-address", api.PayoutAddress)
	mux.HandleFunc("GET /api/customer", api.Customer)
	mux.HandleFunc("GET /api/device/me", api.DeviceMe)
	mux.HandleFunc("GET /api/network", api.Network)
	mux.HandleFunc("GET /api/catalog", api.Catalog)
	mux.HandleFunc("GET /metrics", mon.Metrics(gw))
	mux.HandleFunc("GET /api/status", mon.StatusJSON(gw))
	mux.HandleFunc("GET /status", mon.StatusPage(gw))
	mux.HandleFunc("POST /api/waitlist", api.JoinWaitlist)
	mux.HandleFunc("GET /api/waitlist/{code}", api.WaitlistStatus)
	mux.HandleFunc("GET /api/leaderboard", api.Leaderboard)
	mux.HandleFunc("GET /api/admin/waitlist", api.AdminWaitlist)
	mux.HandleFunc("POST /api/admin/waitlist/invite", api.AdminWaitlist)
	mux.HandleFunc("GET /s/{tag}", api.SharePage)
	mux.HandleFunc("GET /pair/{code}", api.PairPage)
	mux.HandleFunc("GET /install.sh", func(w http.ResponseWriter, r *http.Request) {
		b, _ := webFS.ReadFile("web/install.sh")
		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		w.Write(b)
	})
	mux.HandleFunc("GET /download", func(w http.ResponseWriter, r *http.Request) {
		b, _ := webFS.ReadFile("web/download.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})
	mux.HandleFunc("GET /api/admin/payouts", api.PayoutBatch)
	mux.HandleFunc("POST /api/admin/payouts/mark-sent", api.MarkPaid)
	mux.HandleFunc("POST /api/admin/credits", api.GrantCredits)
	mux.HandleFunc("POST /api/owner/appeal", api.FileAppeal)
	mux.HandleFunc("POST /api/owner/payout-address/verify", api.VerifyWallet)
	mux.HandleFunc("POST /api/admin/verify", api.AdminVerify)
	mux.HandleFunc("GET /api/admin/appeals", api.AdminAppeals)
	mux.HandleFunc("POST /api/admin/appeals/resolve", api.AdminAppeals)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServer(http.FS(sub)))

	addr := env("INFERA_ADDR", ":8080")
	if os.Getenv("INFERA_ADDR") == "" && os.Getenv("PORT") != "" { // Railway, Render, Fly set PORT
		addr = ":" + os.Getenv("PORT")
	}
	log.Printf("Infera control plane on %s (owner share %.0f%%, fallback %v)", addr, cfg.OwnerShare*100, cfg.FallbackURL != "")
	srv := &http.Server{Addr: addr, Handler: mon.Wrap(logRequests(mux)), ReadHeaderTimeout: 15 * time.Second}
	// Graceful stop: Railway (and Docker) send SIGTERM to the old container on every deploy.
	// Exit 0 after finishing in-flight requests, so it isn't reported as a crash.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-stop
		log.Printf("shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	log.Printf("stopped")
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" && r.URL.Path != "/ws/node" && r.URL.Path != "/api/device/me" {
			log.Printf("%s %s", r.Method, r.URL.Path)
		}
		h.ServeHTTP(w, r)
	})
}
