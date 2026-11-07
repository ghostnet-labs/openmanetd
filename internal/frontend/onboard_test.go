package frontend

import (
	"archive/zip"
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openmanet/openmanetd/internal/auth"
	"github.com/openmanet/openmanetd/internal/onboard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testAPK(t *testing.T) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	for _, n := range []string{"AndroidManifest.xml", "classes.dex"} {
		w, err := zw.Create(n)
		require.NoError(t, err)

		_, err = w.Write(bytes.Repeat([]byte(n), 64))
		require.NoError(t, err)
	}

	require.NoError(t, zw.Close())

	return buf.Bytes()
}

// newOnboardServer returns a frontend handler with an onboarding store in
// a temp dir. With authOn the frontend auth middleware is active and no
// session exists.
func newOnboardServer(t *testing.T, authOn bool) (*httptest.Server, *onboard.Store) {
	t.Helper()

	st, err := onboard.NewStore(filepath.Join(t.TempDir(), "onboard"), 1<<20)
	require.NoError(t, err)

	srv := newTestServer(func(s *Server) {
		s.onboard = st
		if authOn {
			s.authEnabled = true
			s.sessionStore = auth.NewSessionStore(time.Hour, 4)
		}
	})

	ts := httptest.NewServer(srv.handler())
	t.Cleanup(ts.Close)

	return ts, st
}

func uploadBody(t *testing.T, field string, data []byte) (*bytes.Buffer, string) {
	t.Helper()

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)
	require.NoError(t, mw.WriteField("note", "ignored"))

	fw, err := mw.CreateFormFile(field, "ATAK-CIV-5.6.0.apk")
	require.NoError(t, err)

	_, err = fw.Write(data)
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	return &body, mw.FormDataContentType()
}

func do(t *testing.T, method, url string, body io.Reader, header http.Header) (*http.Response, []byte) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, url, body)
	require.NoError(t, err)

	for k, v := range header {
		req.Header[k] = v
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	return resp, b
}

func TestOnboardStatus_noAPK(t *testing.T) {
	ts, _ := newOnboardServer(t, false)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://10.41.3.7:8081/onboard/status", nil)
	w := httptest.NewRecorder()
	ts.Config.Handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))

	var st onboardStatus
	decodeJSON(t, w.Body, &st)

	assert.False(t, st.APK.Available)
	assert.Equal(t, "http://10.41.3.7:8080/onboard/atak.apk", st.APKURL, "plain HTTP on the address the phone used")
	assert.Equal(t, "http://10.41.3.7:8080/onboard/atak-setup.zip", st.PackageURL)
	assert.Contains(t, st.Teams, "Cyan")
	assert.Contains(t, st.Roles, "Team Member")
	assert.Equal(t, []string{"Manet Satellite", "Manet Topo"}, st.MapSources)

	host, err := os.Hostname()
	require.NoError(t, err)
	assert.Equal(t, host, st.Node, "the join page shows which node the phone reached")

	resp, _ := do(t, http.MethodGet, ts.URL+onboardAPKPath, nil, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestOnboard_uploadThenDownload(t *testing.T) {
	ts, _ := newOnboardServer(t, false)
	apk := testAPK(t)

	body, ct := uploadBody(t, "file", apk)
	resp, b := do(t, http.MethodPost, ts.URL+onboardAdminPath, body, http.Header{"Content-Type": {ct}})
	require.Equal(t, http.StatusOK, resp.StatusCode, "body=%s", b)
	assert.Contains(t, string(b), `"name":"ATAK-CIV-5.6.0.apk"`)

	resp, got := do(t, http.MethodGet, ts.URL+onboardAPKPath, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, apkMIME, resp.Header.Get("Content-Type"))
	assert.Contains(t, resp.Header.Get("Content-Disposition"), "ATAK-CIV.apk")
	assert.NotEmpty(t, resp.Header.Get("ETag"))
	assert.Equal(t, apk, got)

	// A dropped download resumes from where it stopped.
	resp, part := do(t, http.MethodGet, ts.URL+onboardAPKPath, nil, http.Header{"Range": {"bytes=10-"}})
	require.Equal(t, http.StatusPartialContent, resp.StatusCode)
	assert.Equal(t, apk[10:], part)

	resp, b = do(t, http.MethodGet, ts.URL+onboardStatusPath, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(b), `"available":true`)

	resp, _ = do(t, http.MethodDelete, ts.URL+onboardAdminPath, nil, nil)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	resp, _ = do(t, http.MethodGet, ts.URL+onboardAPKPath, nil, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestOnboard_uploadErrors(t *testing.T) {
	ts, _ := newOnboardServer(t, false)

	tests := []struct {
		name  string
		field string
		data  []byte
		ctype string
		want  int
	}{
		{name: "not multipart", ctype: "application/octet-stream", want: http.StatusBadRequest},
		{name: "wrong field", field: "apk", data: testAPK(t), want: http.StatusBadRequest},
		{name: "not an apk", field: "file", data: []byte("hello"), want: http.StatusBadRequest},
		{name: "too large", field: "file", data: bytes.Repeat([]byte("a"), 3<<20), want: http.StatusRequestEntityTooLarge},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				body io.Reader = strings.NewReader("raw")
				ct             = tc.ctype
			)

			if tc.field != "" {
				body, ct = uploadBody(t, tc.field, tc.data)
			}

			resp, b := do(t, http.MethodPost, ts.URL+onboardAdminPath, body, http.Header{"Content-Type": {ct}})
			assert.Equal(t, tc.want, resp.StatusCode, "body=%s", b)
		})
	}

	resp, _ := do(t, http.MethodPut, ts.URL+onboardAdminPath, nil, nil)
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

func TestOnboardPackage(t *testing.T) {
	ts, _ := newOnboardServer(t, false)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"http://[fd00::7]:8080/onboard/atak-setup.zip?callsign=Alpha+1&team=Cyan&role=Medic", nil)
	w := httptest.NewRecorder()
	ts.Config.Handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	assert.Equal(t, "application/zip", w.Header().Get("Content-Type"))

	data := w.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)

	files := make(map[string]string, len(zr.File))

	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)

		b, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())

		files[f.Name] = string(b)
	}

	assert.Contains(t, files["prefs/openmanet.pref"], ">Alpha 1<")
	assert.Contains(t, files["mapsources/satellite.xml"], "<url>http://[fd00::7]:8080/tiles/satellite/{$z}/{$x}/{$y}</url>")

	resp, b := do(t, http.MethodGet, ts.URL+onboardPackagePath+"?team=Pink", nil, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, string(b), "unknown team")
}

func TestOnboard_publicRoutesSkipLogin(t *testing.T) {
	ts, _ := newOnboardServer(t, true)

	for _, p := range []string{onboardStatusPath, onboardPackagePath} {
		resp, b := do(t, http.MethodGet, ts.URL+p, nil, nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode, "%s body=%s", p, b)
	}

	resp, _ := do(t, http.MethodGet, ts.URL+onboardAPKPath, nil, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "public, but nothing uploaded")

	body, ct := uploadBody(t, "file", testAPK(t))
	resp, _ = do(t, http.MethodPost, ts.URL+onboardAdminPath, body, http.Header{"Content-Type": {ct}})
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "uploads need a login")

	resp, _ = do(t, http.MethodDelete, ts.URL+onboardAdminPath, nil, nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestOnboard_disabledHasNoRoutes(t *testing.T) {
	srv := newTestServer()
	ts := httptest.NewServer(srv.handler())
	t.Cleanup(ts.Close)

	resp, b := do(t, http.MethodGet, ts.URL+onboardStatusPath, nil, nil)
	assert.NotContains(t, string(b), "apkUrl", "status=%d", resp.StatusCode)
}
