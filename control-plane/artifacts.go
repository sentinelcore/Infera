package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Artifacts: files that multi-step jobs pass between devices (video shots, voice-overs,
// finished videos). Devices upload and download them directly over HTTPS with per-file
// signed links, so large files never travel inside job messages.
//
//   PUT /api/artifacts/{id}?t=<token>   upload (once)
//   GET /api/artifacts/{id}?t=<token>   download
//
// Stored on the server's volume (INFERA_ARTIFACTS, default <volume>/artifacts) for 48 hours.

const artifactKeep = 48 * time.Hour
const maxArtifact = 400 << 20

var artifactID = regexp.MustCompile(`^[a-z0-9_]{8,64}$`)

type Artifacts struct {
	dir    string
	secret []byte
	base   string // public base URL devices use, e.g. https://infera.valanor.network
}

func NewArtifacts() *Artifacts {
	dir := os.Getenv("INFERA_ARTIFACTS")
	if dir == "" {
		if v := os.Getenv("RAILWAY_VOLUME_MOUNT_PATH"); v != "" {
			dir = filepath.Join(v, "artifacts")
		} else {
			dir = "artifacts"
		}
	}
	os.MkdirAll(dir, 0o755)
	secret := os.Getenv("INFERA_ARTIFACT_SECRET")
	if secret == "" { // stable across restarts so links in flight keep working
		secret = "artifacts|" + os.Getenv("INFERA_ADMIN_TOKEN") + "|" + os.Getenv("INFERA_DATABASE_URL") + os.Getenv("INFERA_DB")
	}
	base := strings.TrimRight(os.Getenv("INFERA_PUBLIC_URL"), "/")
	if base == "" {
		port := os.Getenv("PORT")
		if port == "" {
			port = "8080"
		}
		base = "http://localhost:" + port
	}
	a := &Artifacts{dir: dir, secret: []byte(secret), base: base}
	go a.cleanLoop()
	return a
}

func (a *Artifacts) token(id string) string {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte(id))
	return hex.EncodeToString(m.Sum(nil))[:40]
}

// New returns a fresh artifact id and the signed link to upload / download it.
func (a *Artifacts) New(prefix string) (id, url string) {
	id = prefix + "_" + randomHex(12)
	return id, a.URL(id)
}

func (a *Artifacts) URL(id string) string {
	return a.base + "/api/artifacts/" + id + "?t=" + a.token(id)
}

func (a *Artifacts) Path(id string) string { return filepath.Join(a.dir, id) }

func (a *Artifacts) Exists(id string) bool {
	st, err := os.Stat(a.Path(id))
	return err == nil && st.Size() > 0
}

func (a *Artifacts) ok(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !artifactID.MatchString(id) || !hmac.Equal([]byte(r.URL.Query().Get("t")), []byte(a.token(id))) {
		apiError(w, 403, "bad or expired link")
		return "", false
	}
	return id, true
}

func (a *Artifacts) Put(w http.ResponseWriter, r *http.Request) {
	id, ok := a.ok(w, r)
	if !ok {
		return
	}
	if a.Exists(id) {
		apiError(w, 409, "already uploaded")
		return
	}
	tmp := a.Path(id) + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		apiError(w, 500, "storage unavailable")
		return
	}
	n, err := io.Copy(f, io.LimitReader(r.Body, maxArtifact+1))
	f.Close()
	if err != nil || n == 0 || n > maxArtifact {
		os.Remove(tmp)
		apiError(w, 400, "upload failed or larger than 400 MB")
		return
	}
	if err := os.Rename(tmp, a.Path(id)); err != nil {
		apiError(w, 500, "storage unavailable")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "bytes": n})
}

func (a *Artifacts) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := a.ok(w, r)
	if !ok {
		return
	}
	if !a.Exists(id) {
		apiError(w, 404, "not uploaded yet")
		return
	}
	http.ServeFile(w, r, a.Path(id))
}

func (a *Artifacts) cleanLoop() {
	for {
		entries, _ := os.ReadDir(a.dir)
		for _, e := range entries {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > artifactKeep {
				os.Remove(filepath.Join(a.dir, e.Name()))
			}
		}
		time.Sleep(time.Hour)
	}
}
