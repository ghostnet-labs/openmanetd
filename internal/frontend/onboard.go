package frontend

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/onboard"
)

// Phone onboarding endpoints.
//
// Public (no login), so a phone that has only just joined the node's
// access point can use them:
//
//	GET /onboard/status          what this node offers, with the URLs to use
//	GET /onboard/atak.apk        the ATAK APK (supports Range, so a dropped
//	                             download resumes)
//	GET /onboard/atak-setup.zip  ATAK data package; ?callsign=&team=&role=
//
// Behind login:
//
//	POST   /api/onboard/atak     upload an APK (multipart field "file")
//	DELETE /api/onboard/atak     remove it
const (
	onboardStatusPath  = "/onboard/status"
	onboardAPKPath     = "/onboard/atak.apk"
	onboardPackagePath = "/onboard/atak-setup.zip"
	onboardAdminPath   = "/api/onboard/atak"
	// onboardFormSlack covers multipart headers around the APK bytes.
	onboardFormSlack = 1 << 20
	apkMIME          = "application/vnd.android.package-archive"
)

type onboardAPKStatus struct {
	Uploaded  *time.Time `json:"uploaded,omitempty"`
	Name      string     `json:"name,omitempty"`
	SHA256    string     `json:"sha256,omitempty"`
	Size      int64      `json:"size,omitempty"`
	Available bool       `json:"available"`
}

// onboardStatus is the /onboard/status body. APKURL and PackageURL are
// plain-HTTP URLs on the address the phone used: ATAK fetches the
// package itself and does not trust the node's self-signed HTTPS
// certificate, so it must be HTTP.
type onboardStatus struct {
	Teams      []string         `json:"teams"`
	Roles      []string         `json:"roles"`
	MapSources []string         `json:"mapSources"`
	APKURL     string           `json:"apkUrl"`
	PackageURL string           `json:"packageUrl"`
	APK        onboardAPKStatus `json:"apk"`
}

// newOnboardStore opens the APK store, or returns nil when onboarding is
// disabled or its directory cannot be created.
func newOnboardStore(cfg *config.Config, s *Server) *onboard.Store {
	oc := cfg.GetOnboard()
	if !oc.Enable {
		return nil
	}

	st, err := onboard.NewStore(oc.Dir, oc.MaxAPKBytes)
	if err != nil {
		s.log.Warn().Err(err).Str("dir", oc.Dir).Msg("phone onboarding disabled")

		return nil
	}

	return st
}

// registerOnboardPublic adds the no-login onboarding routes to root.
func (s *Server) registerOnboardPublic(root *http.ServeMux) {
	if s.onboard == nil {
		return
	}

	root.HandleFunc(onboardStatusPath, s.handleOnboardStatus)
	root.HandleFunc(onboardAPKPath, s.handleOnboardAPK)
	root.HandleFunc(onboardPackagePath, s.handleOnboardPackage)
}

// registerOnboardAdmin adds the login-protected onboarding routes to mux.
func (s *Server) registerOnboardAdmin(mux *http.ServeMux) {
	if s.onboard == nil {
		return
	}

	mux.HandleFunc(onboardAdminPath, s.handleOnboardAdmin)
}

// httpBaseURL returns "http://<host the phone used>:<HTTP port>".
func (s *Server) httpBaseURL(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	host = strings.Trim(host, "[]")

	port := "8080"
	if _, p, err := net.SplitHostPort(s.cfg.GetOpenMANETFrontendHostPort()); err == nil && p != "" {
		port = p
	}

	return "http://" + net.JoinHostPort(host, port)
}

func (s *Server) handleOnboardStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeErrorStatus(w, http.StatusMethodNotAllowed, "method not allowed")

		return
	}

	sources := s.cfg.GetOnboard().MapSources
	names := make([]string, 0, len(sources))

	for _, src := range sources {
		names = append(names, src.Name)
	}

	base := s.httpBaseURL(r)
	st := onboardStatus{
		APKURL:     base + onboardAPKPath,
		PackageURL: base + onboardPackagePath,
		Teams:      onboard.Teams(),
		Roles:      onboard.Roles(),
		MapSources: names,
	}

	if info, ok := s.onboard.Info(); ok {
		uploaded := info.Uploaded
		st.APK = onboardAPKStatus{
			Available: true,
			Name:      info.Name,
			Size:      info.Size,
			SHA256:    info.SHA256,
			Uploaded:  &uploaded,
		}
	}

	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, st)
}

func (s *Server) handleOnboardAPK(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		s.writeErrorStatus(w, http.StatusMethodNotAllowed, "method not allowed")

		return
	}

	f, info, err := s.onboard.Open()
	if errors.Is(err, onboard.ErrNoAPK) {
		s.writeErrorStatus(w, http.StatusNotFound, "no ATAK APK on this node yet")

		return
	}

	if err != nil {
		s.log.Error().Err(err).Msg("open onboard apk")
		s.writeErrorStatus(w, http.StatusInternalServerError, "cannot read the APK")

		return
	}

	defer f.Close()

	w.Header().Set("Content-Type", apkMIME)
	w.Header().Set("Content-Disposition", `attachment; filename="ATAK-CIV.apk"`)
	// The hash doubles as a strong ETag so a resumed download is only
	// stitched onto the same file.
	w.Header().Set("ETag", strconv.Quote(info.SHA256))
	http.ServeContent(w, r, "ATAK-CIV.apk", info.Uploaded, f)
}

func (s *Server) handleOnboardPackage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeErrorStatus(w, http.StatusMethodNotAllowed, "method not allowed")

		return
	}

	q := r.URL.Query()
	p := onboard.Profile{
		Callsign: strings.TrimSpace(q.Get("callsign")),
		Team:     q.Get("team"),
		Role:     q.Get("role"),
	}

	if err := p.Validate(); err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err.Error())

		return
	}

	// A package is a few KB, so build it in memory and send it with a
	// Content-Length.
	var buf bytes.Buffer
	if err := onboard.BuildDataPackage(&buf, s.httpBaseURL(r)+"/tiles", p, s.cfg.GetOnboard().MapSources); err != nil {
		s.log.Error().Err(err).Msg("build onboard data package")
		s.writeErrorStatus(w, http.StatusInternalServerError, "cannot build the settings package")

		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="OpenMANET-setup.zip"`)
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) handleOnboardAdmin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleOnboardUpload(w, r)
	case http.MethodDelete:
		if err := s.onboard.Remove(); err != nil {
			s.log.Error().Err(err).Msg("remove onboard apk")
			s.writeErrorStatus(w, http.StatusInternalServerError, "cannot remove the APK")

			return
		}

		w.WriteHeader(http.StatusNoContent)
	default:
		s.writeErrorStatus(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleOnboardUpload streams the "file" part straight to the store.
// It never calls ParseMultipartForm, which would spool the APK to /tmp
// (RAM on OpenWrt).
func (s *Server) handleOnboardUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.GetOnboard().MaxAPKBytes+onboardFormSlack)

	mr, err := r.MultipartReader()
	if err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, "expected a multipart upload")

		return
	}

	for {
		part, err := mr.NextPart()
		if err != nil {
			s.writeErrorStatus(w, http.StatusBadRequest, `missing the "file" field`)

			return
		}

		if part.FormName() != "file" {
			_ = part.Close()

			continue
		}

		info, err := s.onboard.Save(part, part.FileName())
		_ = part.Close()

		var maxErr *http.MaxBytesError

		switch {
		case err == nil:
			s.log.Info().Str("name", info.Name).Int64("size", info.Size).Str("sha256", info.SHA256).Msg("ATAK APK uploaded")
			s.writeJSON(w, info)
		case errors.Is(err, onboard.ErrTooLarge), errors.As(err, &maxErr):
			s.writeErrorStatus(w, http.StatusRequestEntityTooLarge, onboard.ErrTooLarge.Error())
		case errors.Is(err, onboard.ErrNotAPK):
			s.writeErrorStatus(w, http.StatusBadRequest, err.Error())
		default:
			s.log.Error().Err(err).Msg("save onboard apk")
			s.writeErrorStatus(w, http.StatusInternalServerError, "cannot store the APK")
		}

		return
	}
}
